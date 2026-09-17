package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"synon-go/internal/toolcontract"
)

const (
	AskUserPayloadVersion            = 1
	AskUserPromptEventType           = "ask_user_prompt"
	AskUserResultEventType           = "ask_user_result"
	AskUserSelectedRouteInstruction  = "Continue the selected implementation. Repair ordinary execution failures within that exact implementation; explain the cause and ask the user before changing any method, engine, service, or tool."
	AskUserDelegatedRouteInstruction = "The user delegated the implementation choice. Continue the recorded implementation as a model-delegated choice, but do not treat its option text or parameters as explicit user evidence."
)

// AskUserEvidenceResolverSelection records an auxiliary registry-owned route
// chosen by the user without replacing the task's primary implementation.
type AskUserEvidenceResolverSelection struct {
	EvidenceGroup  string `json:"evidence_group"`
	Skill          string `json:"skill"`
	Implementation string `json:"implementation"`
}

func EncodeAnsweredAskUserModelContinuation(answers, implementations map[string]string) (string, error) {
	return EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(answers, implementations, nil)
}

func EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
	answers, implementations map[string]string,
	evidenceResolvers map[string]AskUserEvidenceResolverSelection,
) (string, error) {
	normalizedAnswers, err := normalizedAskUserAnswers(answers)
	if err != nil || len(normalizedAnswers) == 0 {
		return "", errors.New("answered AskUser continuation requires answers")
	}
	var normalizedImplementations map[string]string
	if len(implementations) > 0 {
		normalizedImplementations = make(map[string]string, len(implementations))
		for question, implementation := range implementations {
			question = strings.TrimSpace(question)
			implementation = strings.TrimSpace(implementation)
			if question == "" || implementation == "" || len(implementation) > 4096 {
				return "", errors.New("answered AskUser continuation contains an invalid implementation")
			}
			if _, found := normalizedAnswers[question]; !found {
				return "", errors.New("answered AskUser continuation implementation has no matching answer")
			}
			if _, duplicate := normalizedImplementations[question]; duplicate {
				return "", errors.New("answered AskUser continuation contains duplicate normalized implementations")
			}
			normalizedImplementations[question] = implementation
		}
	}
	var normalizedResolvers map[string]AskUserEvidenceResolverSelection
	if len(evidenceResolvers) > 0 {
		normalizedResolvers = make(map[string]AskUserEvidenceResolverSelection, len(evidenceResolvers))
		for question, resolver := range evidenceResolvers {
			question = strings.TrimSpace(question)
			resolver.EvidenceGroup = strings.TrimSpace(resolver.EvidenceGroup)
			resolver.Skill = strings.TrimSpace(resolver.Skill)
			resolver.Implementation = strings.TrimSpace(resolver.Implementation)
			if question == "" || resolver.EvidenceGroup == "" || resolver.Skill == "" || resolver.Implementation == "" ||
				len(resolver.EvidenceGroup) > 256 || len(resolver.Skill) > 256 || len(resolver.Implementation) > 4096 {
				return "", errors.New("answered AskUser continuation contains an invalid evidence resolver")
			}
			if _, found := normalizedAnswers[question]; !found {
				return "", errors.New("answered AskUser continuation evidence resolver has no matching answer")
			}
			normalizedResolvers[question] = resolver
		}
	}
	encoded, err := json.Marshal(struct {
		Status            AskUserStatus                               `json:"status"`
		Answers           map[string]string                           `json:"answers"`
		Implementations   map[string]string                           `json:"implementations,omitempty"`
		EvidenceResolvers map[string]AskUserEvidenceResolverSelection `json:"evidence_resolvers,omitempty"`
		Instruction       string                                      `json:"instruction"`
	}{
		Status: AskUserStatusAnswered, Answers: normalizedAnswers, Implementations: normalizedImplementations,
		EvidenceResolvers: normalizedResolvers,
		Instruction:       AskUserSelectedRouteInstruction,
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// EncodeDelegatedAskUserModelContinuation keeps a model-delegated route
// distinct from an explicit user answer. The implementation identity may
// still close an execution route, but it can never authorize parameter values
// that were authored by the model inside an AskUser option.
func EncodeDelegatedAskUserModelContinuation(implementations map[string]string) (string, error) {
	normalized := make(map[string]string, len(implementations))
	for question, implementation := range implementations {
		question = strings.TrimSpace(question)
		implementation = strings.TrimSpace(implementation)
		if question == "" || implementation == "" || len(implementation) > 4096 {
			return "", errors.New("delegated AskUser continuation contains an invalid implementation")
		}
		if _, duplicate := normalized[question]; duplicate {
			return "", errors.New("delegated AskUser continuation contains duplicate normalized implementations")
		}
		normalized[question] = implementation
	}
	if len(normalized) == 0 {
		return "", errors.New("delegated AskUser continuation requires an implementation")
	}
	encoded, err := json.Marshal(struct {
		Status          string            `json:"status"`
		Provenance      string            `json:"provenance"`
		Implementations map[string]string `json:"implementations"`
		Instruction     string            `json:"instruction"`
	}{
		Status: "delegated", Provenance: "model-delegated-choice", Implementations: normalized,
		Instruction: AskUserDelegatedRouteInstruction,
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func CanonicalAskUserToolNameV1(name string) (string, bool) {
	return toolcontract.CanonicalAskUser(name)
}

type AskUserAction string

const (
	AskUserActionAnswer      AskUserAction = "answer"
	AskUserActionDecideForMe AskUserAction = "decide_for_me"
	AskUserActionDiscuss     AskUserAction = "discuss"
	AskUserActionCancel      AskUserAction = "cancel"
)

type AskUserStatus string

const (
	AskUserStatusAwaitingResponse AskUserStatus = "awaiting_user_response"
	AskUserStatusAnswered         AskUserStatus = "answered"
	AskUserStatusDeferred         AskUserStatus = "deferred"
	AskUserStatusDiscussed        AskUserStatus = "discussed"
	AskUserStatusCancelled        AskUserStatus = "cancelled"
)

// AskUserResultV1 is the closed durable UI state for one AskUser response.
// Model-facing continuation text is deliberately stored beside, not inside,
// this UI state so both values participate in exact retry identity.
type AskUserResultV1 struct {
	Version int               `json:"version"`
	Status  AskUserStatus     `json:"status"`
	Action  AskUserAction     `json:"action,omitempty"`
	Answers map[string]string `json:"answers,omitempty"`
	Message string            `json:"message,omitempty"`
}

type AskUserOriginV1 struct {
	Version                int    `json:"version"`
	StreamUID              string `json:"stream_uid"`
	Epoch                  int64  `json:"epoch"`
	FrameID                string `json:"frame_id"`
	BranchID               string `json:"branch_id"`
	BranchGeneration       int64  `json:"branch_generation"`
	RunnerAttempt          int64  `json:"runner_attempt"`
	ToolUseID              string `json:"tool_use_id"`
	ToolUseFrameEventID    string `json:"tool_use_frame_event_id"`
	PendingFrameEventID    string `json:"pending_frame_event_id"`
	PromptClientMessageID  string `json:"prompt_client_message_id"`
	PendingClientMessageID string `json:"pending_client_message_id"`
}

type AskUserQuestionOptionV1 struct {
	Label       string         `json:"label"`
	Description string         `json:"description,omitempty"`
	Pros        string         `json:"pros,omitempty"`
	Cons        string         `json:"cons,omitempty"`
	Preview     string         `json:"preview,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type AskUserQuestionV1 struct {
	Question    string                    `json:"question"`
	Header      string                    `json:"header"`
	Options     []AskUserQuestionOptionV1 `json:"options"`
	MultiSelect bool                      `json:"multiSelect"`
}

type AskUserPromptV1 struct {
	Version   int                 `json:"version"`
	Origin    AskUserOriginV1     `json:"origin"`
	ToolUseID string              `json:"tool_use_id"`
	ToolName  string              `json:"tool_name"`
	Questions []AskUserQuestionV1 `json:"questions"`
}

type AskUserResultEventV1 struct {
	Version           int             `json:"version"`
	Origin            AskUserOriginV1 `json:"origin"`
	ToolUseID         string          `json:"tool_use_id"`
	Result            AskUserResultV1 `json:"result"`
	ModelContinuation string          `json:"model_continuation,omitempty"`
}

type AppendFrameAskUserPendingInput struct {
	Claim               RunnerClaim
	ClientMessageID     string
	FrameID             string
	ToolUseID           string
	ToolUseFrameEventID string
	PendingFrameEventID string
}

type AppendFrameAskUserPendingResult struct {
	Prompt  Event
	Pending Event
	Origin  AskUserOriginV1
	Created bool
}

type AppendFrameAskUserResultInput struct {
	OwnerID           string
	FrameID           string
	Origin            AskUserOriginV1
	Result            AskUserResultV1
	ModelContinuation string
	Destinations      []string
}

type GetFrameAskUserPromptInput struct {
	OwnerID string
	FrameID string
	Origin  AskUserOriginV1
}

type FindFrameAskUserTerminalResultInput struct {
	OwnerID          string
	FrameID          string
	StreamUID        string
	BranchID         string
	BranchGeneration int64
	ToolUseID        string
}

type FrameAskUserTerminalResult struct {
	Origin            AskUserOriginV1
	Result            AskUserResultV1
	ModelContinuation string
}

func NewAskUserPendingResultV1() AskUserResultV1 {
	return AskUserResultV1{Version: AskUserPayloadVersion, Status: AskUserStatusAwaitingResponse}
}

func NewAskUserResultV1(action AskUserAction, answers map[string]string, message string) (AskUserResultV1, error) {
	result := AskUserResultV1{
		Version: AskUserPayloadVersion,
		Action:  AskUserAction(strings.TrimSpace(string(action))),
		Answers: answers,
		Message: strings.TrimSpace(message),
	}
	switch result.Action {
	case AskUserActionAnswer:
		result.Status = AskUserStatusAnswered
	case AskUserActionDecideForMe:
		result.Status = AskUserStatusDeferred
	case AskUserActionDiscuss:
		result.Status = AskUserStatusDiscussed
	case AskUserActionCancel:
		result.Status = AskUserStatusCancelled
	default:
		return AskUserResultV1{}, errors.New("unsupported AskUser action")
	}
	return normalizeAskUserResultV1(result)
}

func EncodeAskUserResultV1(result AskUserResultV1) ([]byte, error) {
	normalized, err := normalizeAskUserResultV1(result)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil || len(encoded) > maxEventPayloadBytes {
		return nil, errors.New("AskUser result exceeds the durable limit")
	}
	return encoded, nil
}

func DecodeAskUserResultV1(encoded []byte) (AskUserResultV1, error) {
	var result AskUserResultV1
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return AskUserResultV1{}, errors.New("AskUser result is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AskUserResultV1{}, errors.New("AskUser result is invalid")
	}
	return normalizeAskUserResultV1(result)
}

func DecodeAskUserOriginV1(encoded []byte) (AskUserOriginV1, error) {
	var origin AskUserOriginV1
	if err := decodeAskUserEvent(encoded, &origin); err != nil {
		return AskUserOriginV1{}, errors.New("AskUser origin is invalid")
	}
	origin = normalizeAskUserOriginV1(origin)
	if !validAskUserOriginV1(origin) {
		return AskUserOriginV1{}, errors.New("AskUser origin is invalid")
	}
	return origin, nil
}

func DecodeAskUserPromptV1(encoded []byte) (AskUserPromptV1, error) {
	var prompt AskUserPromptV1
	if err := decodeAskUserEvent(encoded, &prompt); err != nil {
		return AskUserPromptV1{}, errors.New("AskUser prompt is invalid")
	}
	origin := normalizeAskUserOriginV1(prompt.Origin)
	if prompt.Version != AskUserPayloadVersion || prompt.Origin != origin || !validAskUserOriginV1(origin) ||
		prompt.ToolUseID != origin.ToolUseID || prompt.ToolName != "ask_user" {
		return AskUserPromptV1{}, errors.New("AskUser prompt is invalid")
	}
	encodedQuestions, err := json.Marshal(prompt.Questions)
	if err != nil {
		return AskUserPromptV1{}, errors.New("AskUser prompt is invalid")
	}
	var rawQuestions []any
	if json.Unmarshal(encodedQuestions, &rawQuestions) != nil {
		return AskUserPromptV1{}, errors.New("AskUser prompt is invalid")
	}
	normalizedQuestions, err := normalizeAskUserQuestions(rawQuestions)
	if err != nil {
		return AskUserPromptV1{}, errors.New("AskUser prompt is invalid")
	}
	normalizedJSON, err := json.Marshal(normalizedQuestions)
	if err != nil || !bytes.Equal(encodedQuestions, normalizedJSON) {
		return AskUserPromptV1{}, errors.New("AskUser prompt is invalid")
	}
	return prompt, nil
}

func DecodeAskUserResultEventV1(encoded []byte) (AskUserResultEventV1, error) {
	var event AskUserResultEventV1
	if err := decodeAskUserEvent(encoded, &event); err != nil {
		return AskUserResultEventV1{}, errors.New("AskUser result event is invalid")
	}
	origin := normalizeAskUserOriginV1(event.Origin)
	if event.Version != AskUserPayloadVersion || event.Origin != origin || !validAskUserOriginV1(origin) ||
		event.ToolUseID != origin.ToolUseID {
		return AskUserResultEventV1{}, errors.New("AskUser result event is invalid")
	}
	normalizedResult, err := normalizeAskUserResultV1(event.Result)
	if err != nil {
		return AskUserResultEventV1{}, errors.New("AskUser result event is invalid")
	}
	storedResult, _ := json.Marshal(event.Result)
	canonicalResult, _ := json.Marshal(normalizedResult)
	if !bytes.Equal(storedResult, canonicalResult) {
		return AskUserResultEventV1{}, errors.New("AskUser result event is invalid")
	}
	continuation := strings.TrimSpace(event.ModelContinuation)
	if event.Result.Status == AskUserStatusAwaitingResponse {
		if continuation != "" {
			return AskUserResultEventV1{}, errors.New("AskUser result event is invalid")
		}
	} else if continuation == "" || continuation != event.ModelContinuation || len(continuation) > 64<<10 {
		return AskUserResultEventV1{}, errors.New("AskUser result event is invalid")
	}
	return event, nil
}

// AskUserResultClientMessageIDV1 returns the deterministic idempotency key for
// the terminal result bound to one immutable pending AskUser fact.
func AskUserResultClientMessageIDV1(origin AskUserOriginV1) (string, error) {
	origin = normalizeAskUserOriginV1(origin)
	if !validAskUserOriginV1(origin) {
		return "", errors.New("AskUser origin is invalid")
	}
	digest := sha256.Sum256([]byte(origin.StreamUID + "\x00" + origin.PendingClientMessageID + "\x00" + origin.ToolUseID))
	return "ask-user-resolution:" + hex.EncodeToString(digest[:16]), nil
}

func normalizeAskUserResultV1(result AskUserResultV1) (AskUserResultV1, error) {
	if result.Version != AskUserPayloadVersion {
		return AskUserResultV1{}, errors.New("unsupported AskUser result version")
	}
	result.Action = AskUserAction(strings.TrimSpace(string(result.Action)))
	result.Status = AskUserStatus(strings.TrimSpace(string(result.Status)))
	result.Message = strings.TrimSpace(result.Message)
	answers, err := normalizedAskUserAnswers(result.Answers)
	if err != nil {
		return AskUserResultV1{}, err
	}
	result.Answers = answers
	switch result.Status {
	case AskUserStatusAwaitingResponse:
		if result.Action != "" || len(result.Answers) != 0 || result.Message != "" {
			return AskUserResultV1{}, errors.New("pending AskUser result contains terminal fields")
		}
	case AskUserStatusAnswered:
		if result.Action != AskUserActionAnswer || len(result.Answers) == 0 || result.Message != "" {
			return AskUserResultV1{}, errors.New("answered AskUser result is incomplete")
		}
	case AskUserStatusDeferred:
		if result.Action != AskUserActionDecideForMe || len(result.Answers) != 0 || result.Message != "" {
			return AskUserResultV1{}, errors.New("deferred AskUser result is invalid")
		}
	case AskUserStatusDiscussed:
		if result.Action != AskUserActionDiscuss || len(result.Answers) != 0 || result.Message == "" || len(result.Message) > 64<<10 {
			return AskUserResultV1{}, errors.New("discussed AskUser result is invalid")
		}
	case AskUserStatusCancelled:
		if result.Action != AskUserActionCancel || len(result.Answers) != 0 || result.Message != "" {
			return AskUserResultV1{}, errors.New("cancelled AskUser result is invalid")
		}
	default:
		return AskUserResultV1{}, errors.New("unsupported AskUser result status")
	}
	return result, nil
}

// AppendFrameAskUserPending writes self-contained prompt and pending facts on
// the exact claimed stream inside the caller's workspace transaction.
func (tx *ImmediateTransaction) AppendFrameAskUserPending(
	ctx context.Context,
	input AppendFrameAskUserPendingInput,
) (AppendFrameAskUserPendingResult, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return AppendFrameAskUserPendingResult{}, errors.New("transcript transaction is required")
	}
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	input.ToolUseFrameEventID = strings.TrimSpace(input.ToolUseFrameEventID)
	input.PendingFrameEventID = strings.TrimSpace(input.PendingFrameEventID)
	if err := validateClaimInput(input.Claim); err != nil {
		return AppendFrameAskUserPendingResult{}, err
	}
	if input.ClientMessageID == "" || len(input.ClientMessageID) > 480 || input.FrameID == "" || input.ToolUseID == "" ||
		len(input.ToolUseID) > 512 || input.ToolUseFrameEventID == "" || input.PendingFrameEventID == "" {
		return AppendFrameAskUserPendingResult{}, errors.New("complete AskUser pending authority is required")
	}
	now := tx.repository.now().UTC()
	stream, err := getStreamConn(ctx, tx.conn, input.Claim.StreamUID, input.Claim.OwnerID)
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID != input.FrameID {
		return AppendFrameAskUserPendingResult{}, ErrEventConflict
	}
	var branchID string
	var generation int64
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, stream.UID,
	).Scan(&branchID, &generation); err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	if !validTranscriptBranchID(branchID) || generation <= 0 {
		return AppendFrameAskUserPendingResult{}, ErrBranchStateStale
	}
	questions, err := validateAskUserFrameFacts(
		ctx, tx.conn, stream.UID, branchID, stream.FrameID, input.ToolUseID,
		input.ToolUseFrameEventID, input.PendingFrameEventID,
	)
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	promptClientID := input.ClientMessageID + ":prompt"
	pendingClientID := input.ClientMessageID + ":pending"
	origin := AskUserOriginV1{
		Version: AskUserPayloadVersion, StreamUID: stream.UID, Epoch: stream.Epoch, FrameID: stream.FrameID,
		BranchID: branchID, BranchGeneration: generation, RunnerAttempt: input.Claim.Attempt,
		ToolUseID: input.ToolUseID, ToolUseFrameEventID: input.ToolUseFrameEventID,
		PendingFrameEventID: input.PendingFrameEventID, PromptClientMessageID: promptClientID,
		PendingClientMessageID: pendingClientID,
	}
	promptPayload, err := json.Marshal(AskUserPromptV1{
		Version: AskUserPayloadVersion, Origin: origin, ToolUseID: input.ToolUseID,
		ToolName: "ask_user", Questions: questions,
	})
	if err != nil || len(promptPayload) > maxEventPayloadBytes {
		return AppendFrameAskUserPendingResult{}, errors.New("AskUser prompt exceeds the durable limit")
	}
	pendingPayload, err := json.Marshal(AskUserResultEventV1{
		Version: AskUserPayloadVersion, Origin: origin, ToolUseID: input.ToolUseID, Result: NewAskUserPendingResultV1(),
	})
	if err != nil || len(pendingPayload) > maxEventPayloadBytes {
		return AppendFrameAskUserPendingResult{}, errors.New("AskUser pending result exceeds the durable limit")
	}
	_, promptFound, err := findEventByClientID(ctx, tx.conn, stream.UID, promptClientID)
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	_, pendingFound, err := findEventByClientID(ctx, tx.conn, stream.UID, pendingClientID)
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	if promptFound != pendingFound {
		return AppendFrameAskUserPendingResult{}, ErrEventConflict
	}
	if _, err := validateClaimConn(ctx, tx.conn, input.Claim, now, !promptFound); err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	attempt := input.Claim.Attempt
	prompt, promptCreated, err := appendEventConn(ctx, tx.conn, stream, eventRecord{
		clientMessageID: promptClientID, eventType: AskUserPromptEventType, source: EventSourcePayload,
		runnerAttempt: &attempt, payloadJSON: promptPayload, createdAt: now,
	})
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	stream, err = getStreamConn(ctx, tx.conn, stream.UID, stream.OwnerID)
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	pending, pendingCreated, err := appendEventConn(ctx, tx.conn, stream, eventRecord{
		clientMessageID: pendingClientID, eventType: AskUserResultEventType, source: EventSourcePayload,
		runnerAttempt: &attempt, payloadJSON: pendingPayload, createdAt: now,
	})
	if err != nil {
		return AppendFrameAskUserPendingResult{}, schemaError(err)
	}
	if promptCreated != pendingCreated {
		return AppendFrameAskUserPendingResult{}, ErrEventConflict
	}
	return AppendFrameAskUserPendingResult{Prompt: prompt, Pending: pending, Origin: origin, Created: promptCreated}, nil
}

// AppendFrameAskUserResult appends one immutable terminal result against the
// exact pending stream and branch generation. Exact retry is a no-op; a
// different result for the same pending fact conflicts.
func (tx *ImmediateTransaction) AppendFrameAskUserResult(
	ctx context.Context,
	input AppendFrameAskUserResultInput,
) (Event, bool, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return Event{}, false, errors.New("transcript transaction is required")
	}
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.ModelContinuation = strings.TrimSpace(input.ModelContinuation)
	result, err := normalizeAskUserResultV1(input.Result)
	if err != nil || result.Status == AskUserStatusAwaitingResponse {
		return Event{}, false, errors.New("terminal AskUser result is required")
	}
	if input.ModelContinuation == "" || len(input.ModelContinuation) > 64<<10 {
		return Event{}, false, errors.New("AskUser model continuation is required")
	}
	origin := normalizeAskUserOriginV1(input.Origin)
	if input.OwnerID == "" || input.FrameID == "" || !validAskUserOriginV1(origin) || origin.FrameID != input.FrameID {
		return Event{}, false, errors.New("complete AskUser result authority is required")
	}
	stream, err := getStreamConn(ctx, tx.conn, origin.StreamUID, input.OwnerID)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID != input.FrameID || stream.Epoch != origin.Epoch {
		return Event{}, false, ErrEventConflict
	}
	pendingEvent, found, err := findEventByClientID(ctx, tx.conn, stream.UID, origin.PendingClientMessageID)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	if !found || pendingEvent.Type != AskUserResultEventType || pendingEvent.Source != EventSourcePayload ||
		pendingEvent.RunnerAttempt == nil || *pendingEvent.RunnerAttempt != origin.RunnerAttempt {
		return Event{}, false, ErrEventConflict
	}
	var pendingPayload AskUserResultEventV1
	if err := decodeAskUserEvent(pendingEvent.PayloadJSON, &pendingPayload); err != nil || pendingPayload.Version != AskUserPayloadVersion ||
		pendingPayload.Origin != origin || pendingPayload.ToolUseID != origin.ToolUseID ||
		pendingPayload.Result.Version != AskUserPayloadVersion || pendingPayload.Result.Status != AskUserStatusAwaitingResponse ||
		pendingPayload.Result.Action != "" || len(pendingPayload.Result.Answers) != 0 || pendingPayload.Result.Message != "" ||
		pendingPayload.ModelContinuation != "" {
		return Event{}, false, ErrEventConflict
	}
	var membership int
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND branch_id=? AND event_id=?`,
		stream.UID, origin.BranchID, pendingEvent.EventID,
	).Scan(&membership); err != nil {
		return Event{}, false, schemaError(err)
	}
	if membership != 1 {
		return Event{}, false, ErrEventConflict
	}
	payload, err := json.Marshal(AskUserResultEventV1{
		Version: AskUserPayloadVersion, Origin: origin, ToolUseID: origin.ToolUseID, Result: result,
		ModelContinuation: input.ModelContinuation,
	})
	if err != nil || len(payload) > maxEventPayloadBytes {
		return Event{}, false, errors.New("AskUser result exceeds the durable limit")
	}
	clientMessageID, err := AskUserResultClientMessageIDV1(origin)
	if err != nil {
		return Event{}, false, ErrEventConflict
	}
	attempt := origin.RunnerAttempt
	record := eventRecord{
		clientMessageID: clientMessageID, eventType: AskUserResultEventType, source: EventSourcePayload,
		runnerAttempt: &attempt, payloadJSON: payload, destinations: input.Destinations, createdAt: tx.repository.now().UTC(),
	}
	if _, found, err := findEventByClientID(ctx, tx.conn, stream.UID, clientMessageID); err != nil {
		return Event{}, false, schemaError(err)
	} else if found {
		event, created, err := appendEventConn(ctx, tx.conn, stream, record)
		return event, created, schemaError(err)
	}
	var activeBranchID string
	var generation int64
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, stream.UID,
	).Scan(&activeBranchID, &generation); err != nil {
		return Event{}, false, schemaError(err)
	}
	if activeBranchID != origin.BranchID || generation != origin.BranchGeneration {
		return Event{}, false, ErrBranchStateStale
	}
	event, created, err := appendEventConn(ctx, tx.conn, stream, record)
	return event, created, schemaError(err)
}

// GetFrameAskUserPrompt returns the immutable typed prompt bound to an exact
// frame stream, runner attempt, and Transcript branch. It never falls back to
// mutable Frame pending metadata.
func (r *Repository) GetFrameAskUserPrompt(
	ctx context.Context,
	input GetFrameAskUserPromptInput,
) (AskUserPromptV1, error) {
	if r == nil || r.db == nil {
		return AskUserPromptV1{}, ErrSchemaUnavailable
	}
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	origin := normalizeAskUserOriginV1(input.Origin)
	if input.OwnerID == "" || input.FrameID == "" || !validAskUserOriginV1(origin) || origin.FrameID != input.FrameID {
		return AskUserPromptV1{}, errors.New("complete AskUser prompt authority is required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return AskUserPromptV1{}, schemaError(err)
	}
	defer conn.Close()
	stream, err := getStreamConn(ctx, conn, origin.StreamUID, input.OwnerID)
	if err != nil {
		return AskUserPromptV1{}, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID != input.FrameID || stream.Epoch != origin.Epoch {
		return AskUserPromptV1{}, ErrEventConflict
	}
	event, found, err := findEventByClientID(ctx, conn, stream.UID, origin.PromptClientMessageID)
	if err != nil {
		return AskUserPromptV1{}, schemaError(err)
	}
	if !found || event.Type != AskUserPromptEventType || event.Source != EventSourcePayload ||
		event.RunnerAttempt == nil || *event.RunnerAttempt != origin.RunnerAttempt {
		return AskUserPromptV1{}, ErrEventConflict
	}
	var membership int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND event_id=?`, stream.UID, origin.BranchID, event.EventID,
	).Scan(&membership); err != nil {
		return AskUserPromptV1{}, schemaError(err)
	}
	if membership != 1 {
		return AskUserPromptV1{}, ErrEventConflict
	}
	prompt, err := DecodeAskUserPromptV1(event.PayloadJSON)
	if err != nil || prompt.Origin != origin {
		return AskUserPromptV1{}, ErrEventConflict
	}
	return prompt, nil
}

// FindFrameAskUserTerminalResult returns the one typed terminal result bound
// to an exact tool use on the active Transcript branch. It never parses Frame
// prose or mutable pending metadata and it treats duplicate terminal facts as
// a durable conflict.
func (r *Repository) FindFrameAskUserTerminalResult(
	ctx context.Context,
	input FindFrameAskUserTerminalResultInput,
) (FrameAskUserTerminalResult, bool, error) {
	if r == nil || r.db == nil {
		return FrameAskUserTerminalResult{}, false, ErrSchemaUnavailable
	}
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.BranchID = strings.TrimSpace(input.BranchID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	if input.OwnerID == "" || input.FrameID == "" || input.StreamUID == "" || input.BranchID == "" ||
		input.BranchGeneration <= 0 || input.ToolUseID == "" {
		return FrameAskUserTerminalResult{}, false, errors.New("complete AskUser terminal result identity is required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return FrameAskUserTerminalResult{}, false, schemaError(err)
	}
	defer conn.Close()
	stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
	if err != nil {
		return FrameAskUserTerminalResult{}, false, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef || stream.FrameID != input.FrameID {
		return FrameAskUserTerminalResult{}, false, ErrEventConflict
	}
	var activeBranchID string
	var activeGeneration int64
	if err := conn.QueryRowContext(ctx, `SELECT active_branch_id,generation
		FROM transcript_branch_state WHERE stream_uid=?`, stream.UID).Scan(
		&activeBranchID, &activeGeneration,
	); err != nil {
		return FrameAskUserTerminalResult{}, false, schemaError(err)
	}
	if activeBranchID != input.BranchID || activeGeneration != input.BranchGeneration {
		return FrameAskUserTerminalResult{}, false, ErrBranchStateStale
	}
	rows, err := conn.QueryContext(ctx, `SELECT event.event_id,event.runner_attempt,event.payload_json
		FROM transcript_events event
		JOIN transcript_branch_events membership
			ON membership.stream_uid=event.stream_uid AND membership.event_id=event.event_id
		WHERE event.stream_uid=? AND membership.branch_id=? AND event.event_type=?
		ORDER BY event.event_id`, stream.UID, input.BranchID, AskUserResultEventType)
	if err != nil {
		return FrameAskUserTerminalResult{}, false, schemaError(err)
	}
	defer rows.Close()
	var terminal FrameAskUserTerminalResult
	found := false
	for rows.Next() {
		var eventID, runnerAttempt int64
		var payloadJSON []byte
		if err := rows.Scan(&eventID, &runnerAttempt, &payloadJSON); err != nil {
			return FrameAskUserTerminalResult{}, false, schemaError(err)
		}
		event, err := DecodeAskUserResultEventV1(payloadJSON)
		if err != nil || event.Origin.StreamUID != stream.UID || event.Origin.Epoch != stream.Epoch ||
			event.Origin.FrameID != input.FrameID || event.Origin.BranchID != input.BranchID ||
			event.Origin.BranchGeneration != input.BranchGeneration ||
			event.Origin.RunnerAttempt != runnerAttempt || event.ToolUseID != event.Origin.ToolUseID {
			return FrameAskUserTerminalResult{}, false, ErrEventConflict
		}
		if event.ToolUseID != input.ToolUseID || event.Result.Status == AskUserStatusAwaitingResponse {
			continue
		}
		if found {
			return FrameAskUserTerminalResult{}, false, ErrEventConflict
		}
		_ = eventID
		terminal = FrameAskUserTerminalResult{
			Origin: event.Origin, Result: event.Result, ModelContinuation: event.ModelContinuation,
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return FrameAskUserTerminalResult{}, false, schemaError(err)
	}
	return terminal, found, nil
}

func normalizeAskUserQuestions(values []any) ([]AskUserQuestionV1, error) {
	if len(values) < 1 || len(values) > 4 {
		return nil, errors.New("AskUser questions are invalid")
	}
	questions := make([]AskUserQuestionV1, 0, len(values))
	seenQuestions := map[string]bool{}
	for _, rawQuestion := range values {
		questionRecord, ok := rawQuestion.(map[string]any)
		if !ok || !hasOnlyAskUserKeys(questionRecord, "question", "header", "options", "multiSelect") {
			return nil, errors.New("AskUser questions are invalid")
		}
		question, questionOK := strictTrimmedAskUserString(questionRecord["question"])
		header, headerOK := strictTrimmedAskUserString(questionRecord["header"])
		if !questionOK || !headerOK || seenQuestions[question] {
			return nil, errors.New("AskUser questions are invalid")
		}
		seenQuestions[question] = true
		multiSelect := false
		if rawMultiSelect, found := questionRecord["multiSelect"]; found {
			var boolOK bool
			multiSelect, boolOK = rawMultiSelect.(bool)
			if !boolOK {
				return nil, errors.New("AskUser questions are invalid")
			}
		}
		rawOptions, ok := questionRecord["options"].([]any)
		if !ok || len(rawOptions) < 2 || len(rawOptions) > 4 {
			return nil, errors.New("AskUser questions are invalid")
		}
		options := make([]AskUserQuestionOptionV1, 0, len(rawOptions))
		seenLabels := map[string]bool{}
		for _, rawOption := range rawOptions {
			optionRecord, ok := rawOption.(map[string]any)
			if !ok || !hasOnlyAskUserKeys(optionRecord, "label", "description", "pros", "cons", "preview", "metadata") {
				return nil, errors.New("AskUser questions are invalid")
			}
			label, labelOK := strictTrimmedAskUserString(optionRecord["label"])
			description, descriptionOK := optionalAskUserString(optionRecord, "description")
			pros, prosOK := optionalAskUserString(optionRecord, "pros")
			cons, consOK := optionalAskUserString(optionRecord, "cons")
			preview, previewOK := optionalAskUserString(optionRecord, "preview")
			if !labelOK || !descriptionOK || !prosOK || !consOK || !previewOK || seenLabels[label] {
				return nil, errors.New("AskUser questions are invalid")
			}
			seenLabels[label] = true
			var metadata map[string]any
			if rawMetadata, found := optionRecord["metadata"]; found {
				metadataRecord, metadataOK := rawMetadata.(map[string]any)
				if !metadataOK {
					return nil, errors.New("AskUser questions are invalid")
				}
				if len(metadataRecord) > 0 {
					encodedMetadata, metadataErr := json.Marshal(metadataRecord)
					if metadataErr != nil || len(encodedMetadata) > 64<<10 {
						return nil, errors.New("AskUser questions are invalid")
					}
					if json.Unmarshal(encodedMetadata, &metadata) != nil {
						return nil, errors.New("AskUser questions are invalid")
					}
				}
			}
			options = append(options, AskUserQuestionOptionV1{
				Label: label, Description: description, Pros: pros, Cons: cons,
				Preview: preview, Metadata: metadata,
			})
		}
		questions = append(questions, AskUserQuestionV1{
			Question: question, Header: header, Options: options, MultiSelect: multiSelect,
		})
	}
	encoded, err := json.Marshal(questions)
	if err != nil || len(encoded) > 64<<10 {
		return nil, errors.New("AskUser questions exceed the durable limit")
	}
	return questions, nil
}

func hasOnlyAskUserKeys(record map[string]any, allowed ...string) bool {
	keys := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		keys[key] = true
	}
	for key := range record {
		if !keys[key] {
			return false
		}
	}
	return true
}

func strictTrimmedAskUserString(value any) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

func optionalAskUserString(record map[string]any, key string) (string, bool) {
	value, found := record[key]
	if !found {
		return "", true
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok
}

func validateAskUserFrameFacts(
	ctx context.Context,
	conn *sql.Conn,
	streamUID, branchID, frameID, toolUseID, toolUseEventID, pendingEventID string,
) ([]AskUserQuestionV1, error) {
	toolOrdinal, err := validateAskUserBranchFrameReference(
		ctx, conn, streamUID, branchID, toolUseEventID, "assistant_message",
	)
	if err != nil {
		return nil, err
	}
	pendingOrdinal, err := validateAskUserBranchFrameReference(
		ctx, conn, streamUID, branchID, pendingEventID, "user_message",
	)
	if err != nil || toolOrdinal >= pendingOrdinal {
		return nil, ErrEventConflict
	}
	var toolFrameID, toolType string
	var toolPayload []byte
	if err := conn.QueryRowContext(ctx, `SELECT frame_id,event_type,payload FROM frame_events WHERE id=?`, toolUseEventID).Scan(
		&toolFrameID, &toolType, &toolPayload,
	); err != nil {
		return nil, err
	}
	if toolFrameID != frameID || toolType != "assistant_message" {
		return nil, ErrEventConflict
	}
	var toolMessage map[string]any
	if json.Unmarshal(toolPayload, &toolMessage) != nil || toolMessage["role"] != "assistant" {
		return nil, ErrEventConflict
	}
	blocks, ok := toolMessage["content"].([]any)
	if !ok || len(blocks) != 1 {
		return nil, ErrEventConflict
	}
	toolBlock, ok := blocks[0].(map[string]any)
	if !ok || toolBlock["type"] != "tool_use" || strings.TrimSpace(stringValueStrict(toolBlock["id"])) != toolUseID ||
		stringValueStrict(toolBlock["name"]) != "ask_user" {
		return nil, ErrEventConflict
	}
	toolInput, ok := toolBlock["input"].(map[string]any)
	if !ok {
		return nil, ErrEventConflict
	}
	rawQuestions, ok := toolInput["questions"].([]any)
	if !ok {
		return nil, ErrEventConflict
	}
	questions, err := normalizeAskUserQuestions(rawQuestions)
	if err != nil {
		return nil, ErrEventConflict
	}

	var pendingFrameID, pendingType string
	var pendingPayload []byte
	if err := conn.QueryRowContext(ctx, `SELECT frame_id,event_type,payload FROM frame_events WHERE id=?`, pendingEventID).Scan(
		&pendingFrameID, &pendingType, &pendingPayload,
	); err != nil {
		return nil, err
	}
	if pendingFrameID != frameID || pendingType != "user_message" {
		return nil, ErrEventConflict
	}
	var pendingMessage map[string]any
	if json.Unmarshal(pendingPayload, &pendingMessage) != nil || pendingMessage["role"] != "user" {
		return nil, ErrEventConflict
	}
	pendingBlocks, ok := pendingMessage["content"].([]any)
	if !ok || len(pendingBlocks) != 1 {
		return nil, ErrEventConflict
	}
	pendingBlock, ok := pendingBlocks[0].(map[string]any)
	if !ok || pendingBlock["type"] != "tool_result" ||
		strings.TrimSpace(stringValueStrict(pendingBlock["tool_use_id"])) != toolUseID || pendingBlock["is_error"] != true {
		return nil, ErrEventConflict
	}
	content := stringValueStrict(pendingBlock["content"])
	var state map[string]any
	if json.Unmarshal([]byte(content), &state) != nil || len(state) != 1 || state["status"] != string(AskUserStatusAwaitingResponse) {
		return nil, ErrEventConflict
	}
	return questions, nil
}

func validateAskUserBranchFrameReference(
	ctx context.Context,
	conn *sql.Conn,
	streamUID, branchID, frameEventID, eventType string,
) (int64, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT membership.ordinal,event.event_type,event.source
		FROM transcript_branch_events membership
		JOIN transcript_events event
			ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
		WHERE membership.stream_uid=? AND membership.branch_id=? AND event.frame_event_id=?`,
		streamUID, branchID, frameEventID,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	var ordinal int64
	for rows.Next() {
		var storedType, source string
		if err := rows.Scan(&ordinal, &storedType, &source); err != nil {
			return 0, err
		}
		count++
		if storedType != eventType || source != string(EventSourceFrameRef) {
			return 0, ErrEventConflict
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if count != 1 || ordinal <= 0 {
		return 0, ErrEventConflict
	}
	return ordinal, nil
}

func stringValueStrict(value any) string {
	text, _ := value.(string)
	return text
}

func normalizeAskUserOriginV1(origin AskUserOriginV1) AskUserOriginV1 {
	origin.StreamUID = strings.TrimSpace(origin.StreamUID)
	origin.FrameID = strings.TrimSpace(origin.FrameID)
	origin.BranchID = strings.TrimSpace(origin.BranchID)
	origin.ToolUseID = strings.TrimSpace(origin.ToolUseID)
	origin.ToolUseFrameEventID = strings.TrimSpace(origin.ToolUseFrameEventID)
	origin.PendingFrameEventID = strings.TrimSpace(origin.PendingFrameEventID)
	origin.PromptClientMessageID = strings.TrimSpace(origin.PromptClientMessageID)
	origin.PendingClientMessageID = strings.TrimSpace(origin.PendingClientMessageID)
	return origin
}

func validAskUserOriginV1(origin AskUserOriginV1) bool {
	return origin.Version == AskUserPayloadVersion && origin.StreamUID != "" && len(origin.StreamUID) <= 512 &&
		origin.Epoch > 0 && origin.FrameID != "" && len(origin.FrameID) <= 512 &&
		validTranscriptBranchID(origin.BranchID) && origin.BranchGeneration > 0 && origin.RunnerAttempt > 0 &&
		origin.ToolUseID != "" && len(origin.ToolUseID) <= 512 &&
		origin.ToolUseFrameEventID != "" && len(origin.ToolUseFrameEventID) <= 512 &&
		origin.PendingFrameEventID != "" && len(origin.PendingFrameEventID) <= 512 &&
		origin.PromptClientMessageID != "" && len(origin.PromptClientMessageID) <= 512 &&
		origin.PendingClientMessageID != "" && len(origin.PendingClientMessageID) <= 512
}

func decodeAskUserEvent(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("AskUser event is invalid")
	}
	return nil
}

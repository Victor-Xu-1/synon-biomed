package detached

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	kernelruntime "synon-go/internal/kernel"
)

const (
	ProtocolVersion = 1

	CommandProbe    = "probe"
	CommandDispatch = "dispatch"
	CommandCancel   = "cancel"
	CommandClose    = "close"

	maxCommandRequestBytes  = 64 * 1024
	maxCommandResponseBytes = 512 * 1024
)

type CommandRequest struct {
	Version           int    `json:"version"`
	RequestID         string `json:"request_id"`
	Command           string `json:"command"`
	BackendID         string `json:"backend_id"`
	BackendGeneration int64  `json:"backend_generation"`
	ExecutionID       string `json:"execution_id,omitempty"`
	ExpectedVersion   int64  `json:"expected_version,omitempty"`
	ControllerEpoch   int64  `json:"controller_epoch"`
	ControllerToken   string `json:"controller_token"`
	DispatchSequence  int64  `json:"dispatch_sequence,omitempty"`
	CancelRequestID   string `json:"cancel_request_id,omitempty"`
}

type CommandResponse struct {
	Version   int                                     `json:"version"`
	RequestID string                                  `json:"request_id"`
	OK        bool                                    `json:"ok"`
	Code      string                                  `json:"code,omitempty"`
	Message   string                                  `json:"message,omitempty"`
	Snapshot  *kernelruntime.BackendExecutionSnapshot `json:"snapshot,omitempty"`
	Dispatch  *kernelruntime.BackendDispatchReceipt   `json:"dispatch,omitempty"`
	Cancel    *kernelruntime.BackendCancelReceipt     `json:"cancel,omitempty"`
}

type CommandError struct {
	Code    string
	Message string
}

func (e *CommandError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "detached kernel command failed"
	}
	return e.Message
}

func EncodeCommandRequest(input CommandRequest) ([]byte, error) {
	input = normalizeCommandRequest(input)
	if err := validateCommandRequest(input); err != nil {
		return nil, err
	}
	return encodeCommandFrame(input, maxCommandRequestBytes)
}

func DecodeCommandRequest(frame []byte) (CommandRequest, error) {
	if len(frame) == 0 || len(frame) > maxCommandRequestBytes || frame[len(frame)-1] != '\n' {
		return CommandRequest{}, errors.New("detached kernel command frame is invalid")
	}
	var input CommandRequest
	if err := decodeClosedCommandJSON(frame[:len(frame)-1], &input); err != nil {
		return CommandRequest{}, err
	}
	input = normalizeCommandRequest(input)
	if err := validateCommandRequest(input); err != nil {
		return CommandRequest{}, err
	}
	canonical, err := EncodeCommandRequest(input)
	if err != nil || !bytes.Equal(canonical, frame) {
		return CommandRequest{}, errors.New("detached kernel command frame is not canonical")
	}
	return input, nil
}

func EncodeCommandResponse(input CommandResponse) ([]byte, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Code = strings.TrimSpace(input.Code)
	input.Message = strings.TrimSpace(input.Message)
	if input.Version != ProtocolVersion || !validProtocolIdentity(input.RequestID, 128) ||
		len(input.Code) > 128 || strings.ContainsAny(input.Code, "\x00\r\n") ||
		len(input.Message) > 4096 || strings.ContainsAny(input.Message, "\x00\r\n") ||
		(input.OK && (input.Code != "" || input.Message != "")) ||
		(!input.OK && (input.Code == "" || input.Message == "")) {
		return nil, errors.New("detached kernel command response is invalid")
	}
	return encodeCommandFrame(input, maxCommandResponseBytes)
}

func DecodeCommandResponse(frame []byte) (CommandResponse, error) {
	if len(frame) == 0 || len(frame) > maxCommandResponseBytes || frame[len(frame)-1] != '\n' {
		return CommandResponse{}, errors.New("detached kernel response frame is invalid")
	}
	var input CommandResponse
	if err := decodeClosedCommandJSON(frame[:len(frame)-1], &input); err != nil {
		return CommandResponse{}, err
	}
	canonical, err := EncodeCommandResponse(input)
	if err != nil || !bytes.Equal(canonical, frame) {
		return CommandResponse{}, errors.New("detached kernel response frame is not canonical")
	}
	return input, nil
}

func normalizeCommandRequest(input CommandRequest) CommandRequest {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Command = strings.TrimSpace(input.Command)
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.ControllerToken = strings.TrimSpace(input.ControllerToken)
	input.CancelRequestID = strings.TrimSpace(input.CancelRequestID)
	return input
}

func validateCommandRequest(input CommandRequest) error {
	if input.Version != ProtocolVersion || !validProtocolIdentity(input.RequestID, 128) ||
		!validProtocolIdentity(input.BackendID, 512) || input.BackendGeneration <= 0 ||
		input.ControllerEpoch <= 0 || len(input.ControllerToken) < 32 || len(input.ControllerToken) > 4096 ||
		strings.ContainsAny(input.ControllerToken, "\x00\r\n") {
		return errors.New("detached kernel command authority is invalid")
	}
	switch input.Command {
	case CommandProbe:
		if !validProtocolIdentity(input.ExecutionID, 512) || input.ExpectedVersion != 0 ||
			input.DispatchSequence != 0 || input.CancelRequestID != "" {
			return errors.New("detached kernel probe command is invalid")
		}
	case CommandDispatch:
		if !validProtocolIdentity(input.ExecutionID, 512) || input.ExpectedVersion <= 0 ||
			input.DispatchSequence <= 0 || input.CancelRequestID != "" {
			return errors.New("detached kernel dispatch command is invalid")
		}
	case CommandCancel:
		if !validProtocolIdentity(input.ExecutionID, 512) || input.ExpectedVersion <= 0 ||
			input.DispatchSequence != 0 || !validProtocolIdentity(input.CancelRequestID, 512) {
			return errors.New("detached kernel cancel command is invalid")
		}
	case CommandClose:
		if input.ExecutionID != "" || input.ExpectedVersion != 0 || input.DispatchSequence != 0 ||
			input.CancelRequestID != "" {
			return errors.New("detached kernel close command is invalid")
		}
	default:
		return errors.New("detached kernel command is unsupported")
	}
	return nil
}

func encodeCommandFrame(value any, limit int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded)+1 > limit {
		return nil, errors.New("detached kernel command exceeds the protocol limit")
	}
	encoded = append(encoded, '\n')
	return encoded, nil
}

func decodeClosedCommandJSON(raw []byte, target any) error {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return errors.New("detached kernel command JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("detached kernel command JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("detached kernel command must contain one JSON object")
	}
	return nil
}

func validProtocolIdentity(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func commandResponseError(requestID, code, message string) CommandResponse {
	return CommandResponse{
		Version: ProtocolVersion, RequestID: requestID, OK: false,
		Code: strings.TrimSpace(code), Message: strings.TrimSpace(message),
	}
}

func commandResponseOK(requestID string) CommandResponse {
	return CommandResponse{Version: ProtocolVersion, RequestID: requestID, OK: true}
}

func boundedProtocolTime(value time.Time) time.Time {
	return value.UTC().Round(0)
}

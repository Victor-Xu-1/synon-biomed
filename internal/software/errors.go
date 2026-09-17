package software

import (
	"strings"

	"synon-go/internal/failurecontract"
)

// OperationError is the provider-independent repair contract returned to the
// agent. It keeps diagnostics structured without teaching the core about any
// particular scientific package or workflow.
type OperationError struct {
	Kind        failurecontract.Kind
	Code        string
	Message     string
	Recovery    string
	Retryable   bool
	RepairScope string
	Cause       error
}

func (e *OperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *OperationError) Error() string {
	if e == nil {
		return "software runtime failed"
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	return "software runtime failed"
}

func WrapOperationError(cause error, code, message, recovery string, retryable bool) error {
	err, _ := NewOperationError(code, message, recovery, retryable).(*OperationError)
	err.Cause = cause
	return err
}

func NewOperationError(code, message, recovery string, _ bool) error {
	return &OperationError{
		Kind: failurecontract.KindForDetailCode(code),
		Code: strings.TrimSpace(code), Message: strings.TrimSpace(message),
		Recovery: strings.TrimSpace(recovery), Retryable: false,
		RepairScope: "same_provider_plan",
	}
}

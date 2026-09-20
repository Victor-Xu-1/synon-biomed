package executionprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
)

const ObservationSchema = "synon.execution-observation.v1"

// Observation is a fully covered source classification with outstanding runtime
// binding obligations. It is NOT an execution permission or a sandbox proof.
// Only the host supplies it to the existing executor, outside model arguments.
type Observation struct {
	Schema       string   `json:"schema"`
	Language     string   `json:"language"`
	SourceSHA256 string   `json:"source_sha256"`
	Operations   []string `json:"operations"`
}

// Registered capabilities include operand restrictions in their native AST
// adapters. Unknown syntax/bindings cannot acquire a capability by omission.
var observationOperations = map[string]string{
	"python.literal": "python", "python.output": "python",
	"r.literal": "r", "r.directory": "r", "r.process": "r", "r.system": "r",
	"bash.directory": "bash", "bash.output": "bash", "bash.format": "bash",
	"powershell.literal": "powershell", "powershell.directory": "powershell", "powershell.output": "powershell",
}

func SourceSHA256(source string) string {
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

func newObservation(language, source string, operations []string) *Observation {
	if len(operations) == 0 || len(operations) > MaxFacts {
		return nil
	}
	operations = slices.Clone(operations)
	slices.Sort(operations)
	operations = slices.Compact(operations)
	for _, operation := range operations {
		if observationOperations[operation] != language {
			return nil
		}
	}
	return &Observation{Schema: ObservationSchema, Language: language, SourceSHA256: SourceSHA256(source), Operations: operations}
}

func (p *Observation) Matches(language, source string) bool {
	if p == nil || p.Schema != ObservationSchema || p.Language != language || len(source) > MaxSourceBytes || p.SourceSHA256 != SourceSHA256(source) {
		return false
	}
	canonical := newObservation(language, source, p.Operations)
	return canonical != nil && slices.Equal(canonical.Operations, p.Operations)
}

func (p *Observation) Clone() *Observation {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Operations = slices.Clone(p.Operations)
	return &copy
}

type observationContextKey struct{}

// WithObservation is an internal host-to-executor contract. No JSON field,
// parameter name, durable model argument or tool schema can populate this key.
func WithObservation(ctx context.Context, plan *Observation) context.Context {
	return context.WithValue(ctx, observationContextKey{}, plan.Clone())
}

func ObservationFromContext(ctx context.Context) *Observation {
	if ctx == nil {
		return nil
	}
	plan, _ := ctx.Value(observationContextKey{}).(*Observation)
	return plan.Clone()
}

var ErrObservationUnproved = errors.New("diagnostic runtime bindings are unproved; scientific implementation selection remains required")

func IsObservationDeclined(result map[string]any) bool {
	if result["schema"] != ObservationSchema || result["status"] != "implementation_selection_required" ||
		result["executed"] != false || result["decision_required"] != true || result["ok"] != false {
		return false
	}
	reason, _ := result["reason"].(string)
	switch reason {
	case "runtime_binding_provenance_unproved", "diagnostic_source_binding_mismatch", "diagnostic_language_binding_mismatch",
		"diagnostic_contract_invalid", "diagnostic_operation_unproved", "diagnostic_binding_unproved",
		"diagnostic_implicit_binding_unproved", "diagnostic_shell_binding_unproved", "diagnostic_shell_startup_unproved",
		"diagnostic_runtime_contract_unavailable":
		return true
	default:
		return false
	}
}

// ObservationDeclined is a non-executing runtime refusal, never evidence that
// unrestricted execution after a scientific selection would be safe.
func ObservationDeclined(reason string) map[string]any {
	return map[string]any{
		"schema": ObservationSchema, "ok": false, "status": "implementation_selection_required",
		"executed": false, "decision_required": true, "reason": reason,
		"message":  ErrObservationUnproved.Error(),
		"recovery": "Inspect the existing runtime and complete the pending implementation choice. An unproved or previously modified interpreter binding cannot use the diagnostic exemption; do not retry unchanged or reset the session implicitly.",
	}
}

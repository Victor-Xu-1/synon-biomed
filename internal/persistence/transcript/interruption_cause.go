package transcript

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const RunnerInterruptionCauseField = "interruption_cause"
const runnerInterruptionCauseSchema = "synon.runner_interruption_cause.v1"

// RunnerInterruptionCause separates display from the complete typed condition.
// A nil condition is explicit legacy data, never reconstructed from its preview.
type RunnerInterruptionCause struct {
	ReasonCode   string
	Detail       string
	Condition    *RunnerCorrectionCondition
	ConditionRef *RunnerCorrectionConditionRef
}

func RunnerInterruptionCausePayload(cause RunnerInterruptionCause) (map[string]any, error) {
	cause.ReasonCode = strings.TrimSpace(cause.ReasonCode)
	cause.Detail = strings.TrimSpace(cause.Detail)
	if !validReasonCode(cause.ReasonCode) || cause.Detail == "" || !utf8.ValidString(cause.Detail) ||
		len(cause.Detail) > maxRunnerInterruptionResumeDetailBytes || strings.ContainsRune(cause.Detail, '\x00') {
		return nil, errors.New("runner interruption cause is invalid")
	}
	payload := map[string]any{"schema": runnerInterruptionCauseSchema, "reason_code": cause.ReasonCode, "detail": cause.Detail}
	if cause.Condition != nil {
		if cause.ConditionRef != nil || cause.Condition.ReasonCode != cause.ReasonCode {
			return nil, errors.New("correction condition conflicts with cause")
		}
		if err := cause.Condition.Validate(); err != nil {
			return nil, err
		}
		payload["schema"] = "synon.runner_interruption_cause.v2"
		payload["condition"] = cause.Condition
	} else if cause.ConditionRef != nil {
		if err := cause.ConditionRef.Validate(); err != nil {
			return nil, err
		}
		payload["schema"] = "synon.runner_interruption_cause.v2"
		payload["condition_ref"] = cause.ConditionRef
	}
	return payload, nil
}

func ParseRunnerInterruptionCause(payload map[string]any) (RunnerInterruptionCause, bool, error) {
	value, present := payload[RunnerInterruptionCauseField]
	if !present {
		return RunnerInterruptionCause{}, false, nil
	}
	record, ok := value.(map[string]any)
	if !ok || (record["schema"] != runnerInterruptionCauseSchema && record["schema"] != "synon.runner_interruption_cause.v2") {
		return RunnerInterruptionCause{}, true, errors.New("runner interruption cause schema is invalid")
	}
	reason, reasonOK := record["reason_code"].(string)
	detail, detailOK := record["detail"].(string)
	if !reasonOK || !detailOK {
		return RunnerInterruptionCause{}, true, errors.New("runner interruption cause fields are invalid")
	}
	cause := RunnerInterruptionCause{ReasonCode: strings.TrimSpace(reason), Detail: strings.TrimSpace(detail)}
	if record["schema"] == runnerInterruptionCauseSchema {
		if len(record) != 3 {
			return cause, true, errors.New("legacy correction cause has unknown fields")
		}
	} else {
		if len(record) != 4 {
			return cause, true, errors.New("typed correction cause has unknown fields")
		}
		if value, found := record["condition"]; found {
			raw, err := json.Marshal(value)
			if err != nil {
				return cause, true, err
			}
			condition, err := DecodeRunnerCorrectionCondition(raw)
			if err != nil {
				return cause, true, err
			}
			cause.Condition = &condition
		} else if value, found := record["condition_ref"]; found {
			ref, err := decodeRunnerCorrectionConditionRef(value)
			if err != nil {
				return cause, true, err
			}
			cause.ConditionRef = &ref
		} else {
			return cause, true, errors.New("typed correction cause is missing its condition")
		}
	}
	if _, err := RunnerInterruptionCausePayload(cause); err != nil {
		return RunnerInterruptionCause{}, true, err
	}
	return cause, true, nil
}

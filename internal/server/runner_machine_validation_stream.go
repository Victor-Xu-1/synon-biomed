package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type runnerMachineScanValue struct {
	state     runnerMachineValidationState
	container bool
	nonempty  bool
}

// Stream arrays and unrelated payloads; retain only validation findings and
// last-value state for object keys that carry findings. This preserves JSON's
// existing duplicate-key semantics without retaining the complete document.
func scanRunnerMachineValidation(ctx context.Context, source io.Reader, name string) ([]string, error) {
	if !runnerMachineValidationArtifactName(name) {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	buffer := bufio.NewReader(&contextReader{ctx: ctx, reader: source})
	if prefix, _ := buffer.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffer.Discard(3)
	}
	decoder := json.NewDecoder(buffer)
	decoder.UseNumber()
	value, err := readRunnerMachineScanValue(ctx, decoder, name, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if runnerJSONDataError(err) {
			return []string{"machine_validation_invalid_json:" + name}, nil
		}
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil && !runnerJSONDataError(err) {
			return nil, err
		}
		return []string{"machine_validation_trailing_data:" + name}, nil
	}
	if !value.container || !value.nonempty {
		return []string{"machine_validation_invalid_root:" + name}, nil
	}
	if !value.state.hasPassingCheck {
		value.state.failures = append(value.state.failures, "machine_validation_missing_passing_check:"+name)
	}
	sort.Strings(value.state.failures)
	return value.state.failures, nil
}

func runnerJSONDataError(err error) bool {
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntax) || errors.As(err, &shape)
}

func readRunnerMachineScanValue(ctx context.Context, decoder *json.Decoder, name, path, key string) (runnerMachineScanValue, error) {
	if err := ctx.Err(); err != nil {
		return runnerMachineScanValue{}, err
	}
	token, err := decoder.Token()
	if err != nil {
		return runnerMachineScanValue{}, err
	}
	value := runnerMachineScanValue{}
	merge := func(child runnerMachineValidationState) {
		value.state.hasPassingCheck = value.state.hasPassingCheck || child.hasPassingCheck
		value.state.failures = append(value.state.failures, child.failures...)
	}
	switch typed := token.(type) {
	case json.Delim:
		value.container = true
		value.nonempty = decoder.More()
		switch typed {
		case '{':
			parts := make(map[string]runnerMachineValidationState)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return runnerMachineScanValue{}, err
				}
				childKey, ok := keyToken.(string)
				if !ok {
					return runnerMachineScanValue{}, fmt.Errorf("invalid object key in machine validation")
				}
				childPath := childKey
				if path != "" {
					childPath = path + "." + childKey
				}
				child, err := readRunnerMachineScanValue(ctx, decoder, name, childPath, childKey)
				if err != nil {
					return runnerMachineScanValue{}, err
				}
				if child.state.hasPassingCheck || len(child.state.failures) > 0 {
					parts[childKey] = child.state
				} else {
					delete(parts, childKey)
				}
			}
			for _, child := range parts {
				merge(child)
			}
		case '[':
			for index := 0; decoder.More(); index++ {
				child, err := readRunnerMachineScanValue(ctx, decoder, name, fmt.Sprintf("%s[%d]", path, index), "")
				if err != nil {
					return runnerMachineScanValue{}, err
				}
				merge(child.state)
			}
		default:
			return runnerMachineScanValue{}, io.ErrUnexpectedEOF
		}
		if _, err := decoder.Token(); err != nil {
			return runnerMachineScanValue{}, err
		}
	case string:
		value.nonempty = strings.TrimSpace(typed) != ""
		if runnerMachineValidationFailureStatusKey(key) {
			if runnerMachineValidationFailureStatus(typed) {
				value.state.failures = append(value.state.failures, fmt.Sprintf("machine_validation_failed_status:%s path=%s value=%s", name, path, strings.TrimSpace(typed)))
			} else if runnerMachineValidationPassingStatus(typed) {
				value.state.hasPassingCheck = true
			}
		}
	case bool:
		if runnerMachineValidationCheckKey(key) {
			if typed {
				value.state.hasPassingCheck = true
			} else {
				value.state.failures = append(value.state.failures, fmt.Sprintf("machine_validation_false_check:%s path=%s", name, path))
			}
		}
	}
	if runnerMachineValidationFailureCollectionKey(key) && value.nonempty {
		value.state.failures = append(value.state.failures, fmt.Sprintf("machine_validation_reported_failure:%s path=%s", name, path))
	}
	return value, nil
}

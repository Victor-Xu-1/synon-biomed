package kernelcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/software"
	"synon-go/internal/software/localconda"
)

// LegacyWorkspaceAlias identifies only the narrow absolute path shapes that
// older model/tool adapters used when they guessed a task workspace. The
// server may map a nonexistent alias to the frame-owned workspace, but no
// arbitrary path is treated as equivalent.
func LegacyWorkspaceAlias(requested string) bool {
	requested = strings.TrimSpace(requested)
	if !filepath.IsAbs(requested) {
		return false
	}
	cleaned := filepath.Clean(requested)
	if strings.EqualFold(filepath.Base(cleaned), "workspace") {
		return true
	}
	return strings.EqualFold(filepath.Base(filepath.Dir(cleaned)), "workspace")
}

// SoftwareRuntimeWorkingDirBound verifies that a physical detached execution
// uses either the exact admitted cwd or the frame workspace selected by the
// server for a narrowly recognized legacy alias. Authorization and filesystem
// existence remain server responsibilities; this function only prevents the
// durable request and its authorized execution from becoming two identities.
func SoftwareRuntimeWorkingDirBound(requested, effective, workspace string) bool {
	requested = strings.TrimSpace(requested)
	effective = strings.TrimSpace(effective)
	workspace = strings.TrimSpace(workspace)
	if requested == effective {
		return true
	}
	if requested != "" && !filepath.IsAbs(requested) &&
		effective != "" && workspace != "" && filepath.IsAbs(effective) && filepath.IsAbs(workspace) {
		cleaned := filepath.Clean(requested)
		if cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return filepath.Clean(effective) == filepath.Clean(filepath.Join(workspace, cleaned))
		}
		return false
	}
	if requested == "" || effective == "" || workspace == "" ||
		!filepath.IsAbs(effective) || !filepath.IsAbs(workspace) {
		return false
	}
	return LegacyWorkspaceAlias(requested) &&
		filepath.Clean(effective) == filepath.Clean(workspace)
}

const (
	PythonTool          = "python"
	RTool               = "r"
	BashTool            = "bash"
	ReplTool            = "repl"
	SoftwareRuntimeTool = "software_runtime"

	maxDurableInputBytes = 1 << 20
)

// IsTool reports whether a model tool call can own a durable local execution.
// Admission still requires CanonicalInput to succeed; an invalid model call is
// retained in the durable tool batch but must never enter the execution ledger.
func IsTool(tool string) bool {
	switch strings.TrimSpace(tool) {
	case PythonTool, RTool, BashTool, ReplTool, SoftwareRuntimeTool:
		return true
	default:
		return false
	}
}

// ValidateSoftwareRuntimeExecutionSource binds the provider-generated
// launcher to the canonical model request without requiring persistence to
// understand provider internals. This is the durable software counterpart to
// matching an ordinary Python operation's exact admitted code string.
func ValidateSoftwareRuntimeExecutionSource(
	raw json.RawMessage,
	environment string,
	source string,
) (software.Request, error) {
	canonical, admittedEnvironment, err := CanonicalInput(SoftwareRuntimeTool, raw)
	if err != nil || !bytes.Equal(canonical, raw) || strings.TrimSpace(environment) != admittedEnvironment {
		return software.Request{}, errors.New("kernel software execution authority is invalid")
	}
	request, err := software.DecodeRequestJSON(canonical)
	if err != nil {
		executionRequest, _, _, executionErr := sciencecapability.DecodeExecutionRequestJSON(canonical)
		if executionErr != nil {
			return software.Request{}, err
		}
		request, _, _, _, err = sciencecapability.BuildSoftwareRequest(executionRequest)
		if err != nil {
			return software.Request{}, err
		}
	}
	if err := localconda.ValidatePythonHarness(request, admittedEnvironment, source); err != nil {
		return software.Request{}, err
	}
	return request, nil
}

// Admitted reports whether an exact model tool call is safe to bind to the
// durable local-operation protocol. Rejected calls remain ordinary tool-batch
// items so their validation error can be returned to the model for repair.
func Admitted(tool string, raw json.RawMessage) bool {
	if !IsTool(tool) {
		return false
	}
	_, _, err := CanonicalInput(tool, raw)
	return err == nil
}

// CanonicalInput applies the shared, side-effect-free admission contract used
// by the runner, transcript receipt validator, and workspace operation store.
// Keeping this contract in one package prevents a call from being admitted by
// one layer while being rejected after the durable checkpoint transaction.
func CanonicalInput(tool string, raw json.RawMessage) ([]byte, string, error) {
	tool = strings.TrimSpace(tool)
	if !IsTool(tool) {
		return nil, "", errors.New("unsupported kernel local operation tool")
	}
	if len(raw) == 0 || len(raw) > maxDurableInputBytes || !utf8.Valid(raw) || ValidateJSON(raw) != nil {
		return nil, "", errors.New("kernel local operation input is invalid")
	}
	if tool == SoftwareRuntimeTool {
		if executionRequest, _, _, executionErr := sciencecapability.DecodeExecutionRequestJSON(raw); executionErr == nil {
			canonical, err := sciencecapability.CanonicalExecutionRequestJSON(executionRequest)
			if err != nil || len(canonical) > maxDurableInputBytes {
				return nil, "", errors.New("kernel scientific execution input exceeds the durable limit")
			}
			environment, err := sciencecapability.ExecutionEnvironmentName(executionRequest)
			if err != nil {
				return nil, "", err
			}
			return canonical, environment, nil
		}
		request, err := software.DecodeRequestJSON(raw)
		if err != nil {
			return nil, "", fmt.Errorf("kernel software operation input is invalid: %w", err)
		}
		providerID := software.LocalProviderID
		if request.Provider != "" && request.Provider != providerID {
			return nil, "", errors.New("kernel software operation provider is unavailable")
		}
		environment, err := software.EnvironmentName(providerID, request)
		if err != nil {
			return nil, "", err
		}
		canonical, err := software.CanonicalRequestJSON(request)
		if err != nil || len(canonical) > maxDurableInputBytes {
			return nil, "", errors.New("kernel software operation input exceeds the durable limit")
		}
		return canonical, environment, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var input map[string]any
	if err := decoder.Decode(&input); err != nil || input == nil || decoder.Decode(&struct{}{}) == nil {
		return nil, "", errors.New("kernel local operation input is invalid")
	}
	allowed := map[string]struct{}{"working_dir": {}, "background": {}, "human_description": {}}
	if tool == BashTool {
		allowed["command"] = struct{}{}
	} else {
		allowed["code"] = struct{}{}
	}
	if tool == ReplTool {
		allowed["fresh"] = struct{}{}
	} else {
		allowed["environment"] = struct{}{}
	}
	for key := range input {
		if _, ok := allowed[key]; !ok {
			return nil, "", fmt.Errorf("kernel local operation input contains an unknown field %q", key)
		}
	}
	sourceField := "code"
	if tool == BashTool {
		sourceField = "command"
	}
	source, ok := input[sourceField].(string)
	if !ok || strings.TrimSpace(source) == "" {
		return nil, "", errors.New("kernel local operation source is invalid")
	}
	if value, found := input["human_description"]; found {
		description, ok := value.(string)
		if !ok || strings.TrimSpace(description) == "" || len([]rune(description)) > 256 || strings.ContainsAny(description, "\x00\r\n") {
			return nil, "", errors.New("kernel local operation human description is invalid")
		}
	} else if tool == BashTool {
		return nil, "", errors.New("kernel local operation human description is required")
	}
	if value, found := input["working_dir"]; found {
		if _, ok := value.(string); !ok {
			return nil, "", errors.New("kernel local operation working directory is invalid")
		}
	}
	if value, found := input["background"]; found {
		if _, ok := value.(bool); !ok {
			return nil, "", errors.New("kernel local operation background flag is invalid")
		}
	}
	environment := ReplTool
	if tool != ReplTool {
		var ok bool
		environment, ok = input["environment"].(string)
		environment = strings.TrimSpace(environment)
		if !ok || environment == "" || len(environment) > 256 {
			return nil, "", errors.New("kernel local operation environment is invalid")
		}
	} else if value, found := input["fresh"]; found {
		if _, ok := value.(bool); !ok {
			return nil, "", errors.New("kernel local operation fresh flag is invalid")
		}
	}
	canonical, err := json.Marshal(input)
	if err != nil || len(canonical) > maxDurableInputBytes {
		return nil, "", errors.New("kernel local operation input exceeds the durable limit")
	}
	return canonical, environment, nil
}

// ValidateJSON rejects duplicate object keys and trailing data before a model
// input can become a durable authority record.
func ValidateJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("kernel local operation input contains trailing JSON")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("kernel local operation object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("kernel local operation input contains a duplicate key")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("kernel local operation object is invalid")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("kernel local operation array is invalid")
		}
	default:
		return errors.New("kernel local operation JSON delimiter is invalid")
	}
	return nil
}

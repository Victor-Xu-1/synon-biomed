package sciencecapability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"synon-go/internal/software"
)

const (
	maxExecutionRequestBytes = 128 << 10
)

type ExecutionRequest struct {
	ExecutionPackID string            `json:"execution_pack_id"`
	Inputs          map[string]string `json:"inputs"`
	Parameters      map[string]any    `json:"parameters"`
	WorkingDir      string            `json:"working_dir,omitempty"`
	TimeoutSeconds  int64             `json:"timeout_seconds,omitempty"`
	Background      bool              `json:"background,omitempty"`
}

type UnavailableExecutionPackError struct {
	PackID string
	Reason string
}

func (err *UnavailableExecutionPackError) Error() string {
	if err == nil {
		return "scientific execution pack is unavailable"
	}
	return fmt.Sprintf("scientific execution pack %s is unavailable: %s", err.PackID, err.Reason)
}

func DecodeExecutionRequestJSON(raw []byte) (ExecutionRequest, Definition, EngineDefinition, error) {
	if len(raw) == 0 || len(raw) > maxExecutionRequestBytes {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, errors.New("scientific execution request size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var request ExecutionRequest
	if err := decoder.Decode(&request); err != nil {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, fmt.Errorf("scientific execution request is invalid: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, errors.New("scientific execution request contains trailing data")
	}
	return NormalizeExecutionRequest(request)
}

func NormalizeExecutionRequest(input ExecutionRequest) (ExecutionRequest, Definition, EngineDefinition, error) {
	catalog, err := DefaultCatalog()
	if err != nil {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, err
	}
	input.ExecutionPackID = strings.TrimSpace(input.ExecutionPackID)
	definition, engine, found := catalog.FindExecutionPack(input.ExecutionPackID)
	if !found {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, errors.New("scientific execution pack is not registered")
	}
	pack := engine.ExecutionPack
	if pack.Mode != "local" {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, &UnavailableExecutionPackError{
			PackID: pack.ID, Reason: pack.UnavailableReason,
		}
	}
	input.WorkingDir = strings.TrimSpace(input.WorkingDir)
	if input.WorkingDir != "" && !safeTaskRelativePath(input.WorkingDir) {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, errors.New("scientific execution working_dir must be task relative")
	}
	// Omitted timeouts are unlimited. The detached runtime and durable task
	// identity survive UI/service interruptions; only an explicit positive
	// deadline may bound a scientific execution.
	if input.TimeoutSeconds < 0 || input.TimeoutSeconds > 7*24*60*60 {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, errors.New("scientific execution timeout is invalid")
	}
	inputByKind := make(map[string]ExecutionInput, len(pack.Inputs))
	for _, binding := range pack.Inputs {
		inputByKind[binding.Kind] = binding
	}
	if len(input.Inputs) != len(inputByKind) {
		return ExecutionRequest{}, Definition{}, EngineDefinition{}, errors.New("scientific execution inputs do not match the pack")
	}
	normalizedInputs := make(map[string]string, len(input.Inputs))
	for kind, path := range input.Inputs {
		binding, found := inputByKind[strings.TrimSpace(kind)]
		path = filepath.ToSlash(strings.TrimSpace(path))
		if !found || !safeTaskRelativePath(path) || !containsString(binding.Extensions, strings.ToLower(filepath.Ext(path))) {
			return ExecutionRequest{}, Definition{}, EngineDefinition{}, fmt.Errorf("scientific execution input %q is invalid", kind)
		}
		normalizedInputs[binding.Kind] = path
	}
	input.Inputs = normalizedInputs
	parameterByName := make(map[string]ExecutionParameter, len(pack.Parameters))
	for _, parameter := range pack.Parameters {
		parameterByName[parameter.Name] = parameter
	}
	for name := range input.Parameters {
		if _, found := parameterByName[name]; !found {
			return ExecutionRequest{}, Definition{}, EngineDefinition{}, fmt.Errorf("scientific execution parameter %q is not registered", name)
		}
	}
	normalizedParameters := make(map[string]any, len(pack.Parameters))
	for _, parameter := range pack.Parameters {
		value, found := input.Parameters[parameter.Name]
		if !found {
			if parameter.Default != nil {
				value, found = parameter.Default, true
			} else if parameter.Required {
				return ExecutionRequest{}, Definition{}, EngineDefinition{}, fmt.Errorf("scientific execution parameter %q is required", parameter.Name)
			}
		}
		if !found {
			continue
		}
		value, err = normalizeExecutionParameterValue(parameter, value)
		if err != nil {
			return ExecutionRequest{}, Definition{}, EngineDefinition{}, err
		}
		normalizedParameters[parameter.Name] = value
	}
	input.Parameters = normalizedParameters
	return input, definition, engine, nil
}

func CanonicalExecutionRequestJSON(input ExecutionRequest) ([]byte, error) {
	normalized, _, _, err := NormalizeExecutionRequest(input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// BuildExecutionPackRuntimeRequest returns the package, import, and executable
// contract of one registered local execution pack without inventing workload
// inputs. Provisioning and post-install warmup use this same authority as the
// eventual scientific execution, so a ready environment cannot diverge from
// the pack that will consume it.
func BuildExecutionPackRuntimeRequest(
	executionPackID string,
	timeoutSeconds int64,
) (software.Request, Definition, EngineDefinition, error) {
	catalog, err := DefaultCatalog()
	if err != nil {
		return software.Request{}, Definition{}, EngineDefinition{}, err
	}
	definition, engine, found := catalog.FindExecutionPack(strings.TrimSpace(executionPackID))
	if !found {
		return software.Request{}, Definition{}, EngineDefinition{}, errors.New("scientific execution pack is not registered")
	}
	if engine.ExecutionPack.Mode != "local" {
		return software.Request{}, Definition{}, EngineDefinition{}, &UnavailableExecutionPackError{
			PackID: engine.ExecutionPack.ID, Reason: engine.ExecutionPack.UnavailableReason,
		}
	}
	request, err := buildExecutionPackRuntimeRequest(definition, engine, timeoutSeconds)
	return request, definition, engine, err
}

func buildExecutionPackRuntimeRequest(
	definition Definition,
	engine EngineDefinition,
	timeoutSeconds int64,
) (software.Request, error) {
	pack := engine.ExecutionPack
	request := software.Request{
		Capability: definition.ID, Provider: pack.Provider, Language: pack.Language,
		Executable: pack.Executable, TimeoutSeconds: timeoutSeconds,
	}
	for _, item := range pack.Packages {
		request.Packages = append(request.Packages, software.PackageRequirement{
			Manager: software.PackageManager(item.Manager), Spec: item.Spec,
		})
	}
	request.Channels = append([]string(nil), pack.Channels...)
	request.Imports = append([]string(nil), pack.Imports...)
	return software.NormalizeRequest(request)
}

func BuildSoftwareRequest(input ExecutionRequest) (software.Request, Definition, EngineDefinition, string, error) {
	normalized, definition, engine, err := NormalizeExecutionRequest(input)
	if err != nil {
		return software.Request{}, Definition{}, EngineDefinition{}, "", err
	}
	pack := engine.ExecutionPack
	script, scriptSHA, err := ExecutionPackScript(pack)
	if err != nil {
		return software.Request{}, Definition{}, EngineDefinition{}, "", err
	}
	request, err := buildExecutionPackRuntimeRequest(definition, engine, normalized.TimeoutSeconds)
	if err != nil {
		return software.Request{}, Definition{}, EngineDefinition{}, "", err
	}
	request.Arguments = []string{"-"}
	request.Stdin = string(script)
	request.WorkingDir = normalized.WorkingDir
	request.Background = normalized.Background
	for _, binding := range pack.Inputs {
		request.Arguments = append(request.Arguments, binding.Argument, normalized.Inputs[binding.Kind])
	}
	for _, parameter := range pack.Parameters {
		value, found := normalized.Parameters[parameter.Name]
		if !found {
			continue
		}
		request.Arguments = append(request.Arguments, parameter.Argument, formatExecutionParameter(value))
	}
	for _, output := range pack.Outputs {
		request.ExpectedOutputs = append(request.ExpectedOutputs, software.OutputWitness{
			Path: output.Path, MinBytes: output.MinBytes, Format: output.Format,
			MinRecords: int64(output.MinRecords), RequiredJSONTrue: append([]string(nil), output.RequiredJSONTrue...),
		})
	}
	for _, comparison := range pack.Comparisons {
		request.Comparisons = append(request.Comparisons, software.TabularComparisonContract{
			Path: comparison.Path, DerivedColumns: append([]string(nil), comparison.DerivedColumns...),
			BasisColumns: append([]string(nil), comparison.BasisColumns...), GroupColumns: append([]string(nil), comparison.GroupColumns...),
		})
	}
	evidence := &software.ScientificEvidenceRequest{
		Engine: engine.ID, EnginePackage: engine.Package, ScoreKind: pack.ScoreKind,
	}
	for _, binding := range pack.Inputs {
		evidence.Inputs = append(evidence.Inputs, software.ScientificFileWitness{
			Kind: binding.Kind, Path: normalized.Inputs[binding.Kind],
		})
	}
	for _, required := range engine.RequiredArtifacts {
		for _, output := range pack.Outputs {
			if output.Kind == required {
				evidence.Artifacts = append(evidence.Artifacts, software.ScientificFileWitness{Kind: output.Kind, Path: output.Path})
				break
			}
		}
	}
	request.ScientificEvidence = evidence
	request, err = software.NormalizeRequest(request)
	return request, definition, engine, scriptSHA, err
}

func ExecutionEnvironmentName(input ExecutionRequest) (string, error) {
	request, _, _, _, err := BuildSoftwareRequest(input)
	if err != nil {
		return "", err
	}
	return software.EnvironmentName(software.LocalProviderID, request)
}

func normalizeExecutionParameterValue(parameter ExecutionParameter, value any) (any, error) {
	var normalized any
	switch parameter.Type {
	case "number":
		number, ok := executionNumber(value)
		if !ok {
			return nil, fmt.Errorf("scientific execution parameter %q must be a number", parameter.Name)
		}
		normalized = number
	case "integer":
		number, ok := executionNumber(value)
		if !ok || number != float64(int64(number)) {
			return nil, fmt.Errorf("scientific execution parameter %q must be an integer", parameter.Name)
		}
		normalized = int64(number)
	case "string":
		text, ok := value.(string)
		if !ok || !validBoundedText(strings.TrimSpace(text), 1024) {
			return nil, fmt.Errorf("scientific execution parameter %q must be a bounded string", parameter.Name)
		}
		normalized = strings.TrimSpace(text)
	case "boolean":
		flag, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("scientific execution parameter %q must be a boolean", parameter.Name)
		}
		normalized = flag
	default:
		return nil, fmt.Errorf("scientific execution parameter %q has an unsupported type", parameter.Name)
	}
	if !executionParameterValueValid(parameter, normalized) {
		return nil, fmt.Errorf("scientific execution parameter %q is outside its registered contract", parameter.Name)
	}
	return normalized, nil
}

func executionNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func formatExecutionParameter(value any) string {
	switch typed := value.(type) {
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func safeTaskRelativePath(value string) bool {
	value = filepath.ToSlash(strings.TrimSpace(value))
	clean := filepath.ToSlash(filepath.Clean(value))
	return value != "" && value == clean && clean != "." && !filepath.IsAbs(value) &&
		!strings.HasPrefix(clean, "../") && !strings.ContainsRune(clean, '\x00')
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

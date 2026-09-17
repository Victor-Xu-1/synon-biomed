package software

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	LocalProviderID    = "local-conda"
	maxPackages        = 256
	maxArguments       = 512
	maxArgumentBytes   = 256 * 1024
	maxStdinBytes      = 1 * 1024 * 1024
	maxOutputWitness   = 256
	maxScientificFiles = 64
	maxQualityChecks   = 128
	maxTimeoutSeconds  = 7 * 24 * 60 * 60
)

type PackageManager string

const (
	PackageManagerConda PackageManager = "conda"
	PackageManagerPip   PackageManager = "pip"
)

type PackageRequirement struct {
	Manager PackageManager `json:"manager"`
	Spec    string         `json:"spec"`
}

type OutputWitness struct {
	Path             string   `json:"path"`
	MinBytes         int64    `json:"min_bytes,omitempty"`
	Format           string   `json:"format,omitempty"`
	MinRecords       int64    `json:"min_records,omitempty"`
	RequiredJSONTrue []string `json:"required_json_true,omitempty"`
}

// TabularComparisonContract makes every derived comparison auditable. Rows
// may be compared only inside a group whose declared physical/statistical
// basis is identical. The trusted launcher validates the table after the
// command exits; prose such as "values are comparable" is never evidence.
type TabularComparisonContract struct {
	Path           string   `json:"path"`
	DerivedColumns []string `json:"derived_columns"`
	BasisColumns   []string `json:"basis_columns"`
	GroupColumns   []string `json:"group_columns,omitempty"`
}

// ScientificFileWitness binds one catalog-defined semantic kind to an exact
// file handled by the unified software runtime. Paths remain relative to the
// authorized working directory; the trusted adapter, not model prose, hashes
// their bytes.
type ScientificFileWitness struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// ScientificEvidenceRequest declares the catalog identity that a successful
// software execution intends to satisfy. The local provider resolves the
// engine package version from the immutable environment and hashes the exact
// executable, code, inputs, optional weights, and declared outputs.
type ScientificEvidenceRequest struct {
	Engine        string                  `json:"engine"`
	EnginePackage string                  `json:"engine_package"`
	ScoreKind     string                  `json:"score_kind"`
	Inputs        []ScientificFileWitness `json:"inputs"`
	Artifacts     []ScientificFileWitness `json:"artifacts"`
	CodePaths     []string                `json:"code_paths,omitempty"`
	WeightsPath   string                  `json:"weights_path,omitempty"`
}

// Request is the single admitted description of software needed for one
// computation. It intentionally contains no shell string: callers choose a
// program and argv, while the selected provider owns installation and launch.
type Request struct {
	Capability string `json:"capability"`
	Provider   string `json:"provider,omitempty"`
	Language   string `json:"language"`
	// SourceEnvironment reuses one provider-owned, immutable environment
	// without allowing a request to install or mutate packages. The selected
	// provider remains responsible for validating the exact name.
	SourceEnvironment string `json:"source_environment,omitempty"`
	// InputSHA256 binds a request to the exact input bytes staged in its
	// authorized working directory. It is intentionally excluded from the
	// environment identity because input data must never create environments.
	InputSHA256        string                      `json:"input_sha256,omitempty"`
	Packages           []PackageRequirement        `json:"packages,omitempty"`
	Channels           []string                    `json:"channels,omitempty"`
	Imports            []string                    `json:"imports,omitempty"`
	Executable         string                      `json:"executable"`
	Arguments          []string                    `json:"args,omitempty"`
	Stdin              string                      `json:"stdin,omitempty"`
	WorkingDir         string                      `json:"working_dir,omitempty"`
	TimeoutSeconds     int64                       `json:"timeout_seconds,omitempty"`
	ExpectedOutputs    []OutputWitness             `json:"expected_outputs,omitempty"`
	Comparisons        []TabularComparisonContract `json:"comparisons,omitempty"`
	ScientificEvidence *ScientificEvidenceRequest  `json:"scientific_evidence,omitempty"`
	Background         bool                        `json:"background,omitempty"`
}

func DecodeRequestJSON(raw []byte) (Request, error) {
	if len(raw) == 0 || len(raw) > 2*1024*1024 || !utf8.Valid(raw) {
		return Request{}, errors.New("software request JSON is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("software request JSON is invalid: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("software request JSON contains trailing data")
	}
	return NormalizeRequest(request)
}

func CanonicalRequestJSON(input Request) ([]byte, error) {
	normalized, err := NormalizeRequest(input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

var (
	requestIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	scientificKind    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	importName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	executableName    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,127}$`)
	channelName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sha256Hex         = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// NormalizeRequest validates every untrusted field and returns a detached,
// stable value suitable for hashing and durable persistence.
func NormalizeRequest(input Request) (Request, error) {
	result := input
	result.Capability = strings.ToLower(strings.TrimSpace(result.Capability))
	result.Provider = strings.ToLower(strings.TrimSpace(result.Provider))
	result.Language = strings.ToLower(strings.TrimSpace(result.Language))
	result.SourceEnvironment = strings.ToLower(strings.TrimSpace(result.SourceEnvironment))
	result.InputSHA256 = strings.ToLower(strings.TrimSpace(result.InputSHA256))
	result.Executable = strings.TrimSpace(result.Executable)
	result.WorkingDir = strings.TrimSpace(result.WorkingDir)
	if !requestIdentifier.MatchString(result.Capability) {
		return Request{}, errors.New("software capability must be a bounded lowercase identifier")
	}
	if result.Provider != "" && !requestIdentifier.MatchString(result.Provider) {
		return Request{}, errors.New("software provider must be a bounded lowercase identifier")
	}
	switch result.Language {
	case "python", "r", "native":
	default:
		return Request{}, errors.New("software language must be python, r, or native")
	}
	if result.SourceEnvironment != "" {
		if result.Language != "python" || !requestIdentifier.MatchString(result.SourceEnvironment) {
			return Request{}, errors.New("source_environment requires a bounded managed Python environment name")
		}
		if len(result.Packages) != 0 || len(result.Channels) != 0 {
			return Request{}, errors.New("source_environment cannot be combined with package installation or channels")
		}
	}
	if result.InputSHA256 != "" && !sha256Hex.MatchString(result.InputSHA256) {
		return Request{}, errors.New("input_sha256 must be a lowercase SHA-256 digest")
	}
	if !executableName.MatchString(result.Executable) || filepath.Base(result.Executable) != result.Executable {
		return Request{}, errors.New("software executable must be a path-free program name")
	}
	if len(result.Packages) > maxPackages {
		return Request{}, errors.New("software package request exceeds the bounded limit")
	}
	result.Packages = append([]PackageRequirement(nil), result.Packages...)
	seenPackages := make(map[string]struct{}, len(result.Packages))
	for index := range result.Packages {
		entry := &result.Packages[index]
		entry.Manager = PackageManager(strings.ToLower(strings.TrimSpace(string(entry.Manager))))
		entry.Spec = strings.TrimSpace(entry.Spec)
		if !requestIdentifier.MatchString(string(entry.Manager)) {
			return Request{}, fmt.Errorf("software package %d has an invalid manager identifier", index)
		}
		if !boundedPackageSpec(entry.Spec) {
			return Request{}, fmt.Errorf("software package %d has an invalid bounded specification", index)
		}
		key := string(entry.Manager) + "\x00" + strings.ToLower(entry.Spec)
		if _, duplicate := seenPackages[key]; duplicate {
			return Request{}, fmt.Errorf("software package %d duplicates an earlier requirement", index)
		}
		seenPackages[key] = struct{}{}
	}
	if result.Language == "native" && len(result.Packages) == 0 {
		return Request{}, errors.New("native software requires at least one explicit managed package")
	}
	var err error
	result.Channels, err = normalizeUniqueStrings(result.Channels, maxPackages, func(value string) bool {
		return channelName.MatchString(value)
	}, "software channel")
	if err != nil {
		return Request{}, err
	}
	result.Imports, err = normalizeUniqueStrings(result.Imports, maxPackages, func(value string) bool {
		return importName.MatchString(value)
	}, "software import witness")
	if err != nil {
		return Request{}, err
	}
	if result.Language != "python" && len(result.Imports) != 0 {
		return Request{}, errors.New("import witnesses require the python software language")
	}
	if len(result.Arguments) > maxArguments {
		return Request{}, errors.New("software argument count exceeds the bounded limit")
	}
	result.Arguments = append([]string(nil), result.Arguments...)
	totalArgumentBytes := 0
	for index, argument := range result.Arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') {
			return Request{}, fmt.Errorf("software argument %d is invalid", index)
		}
		totalArgumentBytes += len(argument)
		if len(argument) > 32*1024 || totalArgumentBytes > maxArgumentBytes {
			return Request{}, errors.New("software arguments exceed the bounded byte limit")
		}
	}
	if !utf8.ValidString(result.Stdin) || strings.ContainsRune(result.Stdin, '\x00') || len(result.Stdin) > maxStdinBytes {
		return Request{}, errors.New("software standard input exceeds the bounded text limit")
	}
	if result.WorkingDir != "" && !filepath.IsAbs(result.WorkingDir) {
		clean := filepath.ToSlash(filepath.Clean(result.WorkingDir))
		if clean == ".." || strings.HasPrefix(clean, "../") ||
			strings.ContainsRune(result.WorkingDir, '\x00') || len(result.WorkingDir) > 4096 {
			return Request{}, errors.New("software working_dir must stay inside the task workspace")
		}
		result.WorkingDir = clean
	}
	// Zero deliberately means no wall-clock deadline. Long scientific jobs are
	// owned by durable execution state and explicit cancellation rather than a
	// hidden default timeout. A positive user-authored deadline remains bounded.
	if result.TimeoutSeconds < 0 || result.TimeoutSeconds > maxTimeoutSeconds {
		return Request{}, errors.New("software timeout_seconds is outside the bounded range")
	}
	if len(result.ExpectedOutputs) > maxOutputWitness {
		return Request{}, errors.New("software output witness count exceeds the bounded limit")
	}
	result.ExpectedOutputs = append([]OutputWitness(nil), result.ExpectedOutputs...)
	seenOutputs := make(map[string]struct{}, len(result.ExpectedOutputs))
	outputFormats := make(map[string]string, len(result.ExpectedOutputs))
	for index := range result.ExpectedOutputs {
		witness := &result.ExpectedOutputs[index]
		witness.Path = filepath.ToSlash(strings.TrimSpace(witness.Path))
		witness.Format = strings.ToLower(strings.TrimSpace(witness.Format))
		clean := filepath.ToSlash(filepath.Clean(witness.Path))
		if witness.Path == "" || filepath.IsAbs(witness.Path) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(witness.Path, '\x00') {
			return Request{}, fmt.Errorf("software output witness %d must be a relative path inside working_dir", index)
		}
		if witness.MinBytes < 0 {
			return Request{}, fmt.Errorf("software output witness %d has a negative minimum size", index)
		}
		if witness.MinRecords < 0 || witness.MinRecords > 10_000_000 {
			return Request{}, fmt.Errorf("software output witness %d has an invalid minimum record count", index)
		}
		switch witness.Format {
		case "", "json", "csv", "tsv", "xyz", "png", "html", "text", "binary":
		default:
			return Request{}, fmt.Errorf("software output witness %d has an unsupported format", index)
		}
		if len(witness.RequiredJSONTrue) > maxQualityChecks {
			return Request{}, fmt.Errorf("software output witness %d has too many JSON assertions", index)
		}
		witness.RequiredJSONTrue = append([]string(nil), witness.RequiredJSONTrue...)
		seenPointers := make(map[string]struct{}, len(witness.RequiredJSONTrue))
		for pointerIndex, raw := range witness.RequiredJSONTrue {
			pointer := strings.TrimSpace(raw)
			if !validJSONPointer(pointer) {
				return Request{}, fmt.Errorf("software output witness %d JSON assertion %d is invalid", index, pointerIndex)
			}
			if _, duplicate := seenPointers[pointer]; duplicate {
				return Request{}, fmt.Errorf("software output witness %d JSON assertion %d is duplicated", index, pointerIndex)
			}
			seenPointers[pointer] = struct{}{}
			witness.RequiredJSONTrue[pointerIndex] = pointer
		}
		inferredFormat := witness.Format
		if inferredFormat == "" {
			inferredFormat = strings.TrimPrefix(strings.ToLower(filepath.Ext(clean)), ".")
		}
		if len(witness.RequiredJSONTrue) != 0 && inferredFormat != "json" {
			return Request{}, fmt.Errorf("software output witness %d JSON assertions require JSON output", index)
		}
		if witness.MinRecords != 0 && inferredFormat != "csv" && inferredFormat != "tsv" && inferredFormat != "xyz" {
			return Request{}, fmt.Errorf("software output witness %d record count requires CSV, TSV, or XYZ output", index)
		}
		witness.Path = clean
		if _, duplicate := seenOutputs[clean]; duplicate {
			return Request{}, fmt.Errorf("software output witness %d duplicates an earlier path", index)
		}
		seenOutputs[clean] = struct{}{}
		outputFormats[clean] = inferredFormat
	}
	if len(result.Comparisons) > maxQualityChecks {
		return Request{}, errors.New("software comparison contract count exceeds the bounded limit")
	}
	result.Comparisons = append([]TabularComparisonContract(nil), result.Comparisons...)
	seenComparisons := make(map[string]struct{}, len(result.Comparisons))
	for index := range result.Comparisons {
		comparison := &result.Comparisons[index]
		comparison.Path = filepath.ToSlash(strings.TrimSpace(comparison.Path))
		clean := filepath.ToSlash(filepath.Clean(comparison.Path))
		format, found := outputFormats[clean]
		if !found || (format != "csv" && format != "tsv") {
			return Request{}, fmt.Errorf("software comparison %d must reference a declared CSV or TSV output", index)
		}
		if _, duplicate := seenComparisons[clean]; duplicate {
			return Request{}, fmt.Errorf("software comparison %d duplicates an earlier output", index)
		}
		comparison.Path = clean
		var err error
		comparison.DerivedColumns, err = normalizeQualityColumns(comparison.DerivedColumns, "derived")
		if err != nil {
			return Request{}, fmt.Errorf("software comparison %d: %w", index, err)
		}
		comparison.BasisColumns, err = normalizeQualityColumns(comparison.BasisColumns, "basis")
		if err != nil {
			return Request{}, fmt.Errorf("software comparison %d: %w", index, err)
		}
		if len(comparison.GroupColumns) != 0 {
			comparison.GroupColumns, err = normalizeQualityColumns(comparison.GroupColumns, "group")
			if err != nil {
				return Request{}, fmt.Errorf("software comparison %d: %w", index, err)
			}
		}
		derivedColumns := make(map[string]struct{}, len(comparison.DerivedColumns))
		for _, column := range comparison.DerivedColumns {
			derivedColumns[strings.ToLower(column)] = struct{}{}
		}
		groupColumns := make(map[string]struct{}, len(comparison.GroupColumns))
		for _, column := range comparison.GroupColumns {
			key := strings.ToLower(column)
			if _, duplicate := derivedColumns[key]; duplicate {
				return Request{}, fmt.Errorf("software comparison %d reuses column %q across roles", index, column)
			}
			groupColumns[key] = struct{}{}
		}
		// A grouping key is already constant by construction inside its group.
		// Treating the same key as a basis column is redundant rather than a
		// semantic conflict, so canonicalize it out before the trusted harness
		// checks the remaining physical/statistical basis. This keeps one stable
		// comparison contract instead of forcing callers to guess role syntax.
		basisColumns := make([]string, 0, len(comparison.BasisColumns))
		for _, column := range comparison.BasisColumns {
			key := strings.ToLower(column)
			if _, duplicate := derivedColumns[key]; duplicate {
				return Request{}, fmt.Errorf("software comparison %d reuses column %q across roles", index, column)
			}
			if _, redundant := groupColumns[key]; redundant {
				continue
			}
			basisColumns = append(basisColumns, column)
		}
		if len(basisColumns) == 0 {
			return Request{}, fmt.Errorf("software comparison %d has no non-group basis columns", index)
		}
		comparison.BasisColumns = basisColumns
		seenComparisons[clean] = struct{}{}
	}
	if result.ScientificEvidence != nil {
		evidence, err := normalizeScientificEvidence(*result.ScientificEvidence, result.Packages)
		if err != nil {
			return Request{}, err
		}
		for _, artifact := range evidence.Artifacts {
			if _, found := seenOutputs[artifact.Path]; !found {
				return Request{}, fmt.Errorf("scientific artifact %q must also be a declared expected output", artifact.Path)
			}
		}
		result.ScientificEvidence = &evidence
	}
	return result, nil
}

func validJSONPointer(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || value[0] != '/' || strings.ContainsRune(value, '\x00') {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

func normalizeQualityColumns(values []string, label string) ([]string, error) {
	if len(values) == 0 || len(values) > maxQualityChecks {
		return nil, fmt.Errorf("%s column count is invalid", label)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		key := strings.ToLower(value)
		if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("%s column %d is invalid", label, index)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s column %d is duplicated", label, index)
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeScientificEvidence(
	input ScientificEvidenceRequest,
	packages []PackageRequirement,
) (ScientificEvidenceRequest, error) {
	result := input
	result.Engine = strings.ToLower(strings.TrimSpace(result.Engine))
	result.EnginePackage = strings.ToLower(strings.TrimSpace(result.EnginePackage))
	result.ScoreKind = strings.ToLower(strings.TrimSpace(result.ScoreKind))
	result.WeightsPath = strings.TrimSpace(result.WeightsPath)
	if !scientificKind.MatchString(result.Engine) ||
		!requestIdentifier.MatchString(result.EnginePackage) ||
		!scientificKind.MatchString(result.ScoreKind) {
		return ScientificEvidenceRequest{}, errors.New("scientific evidence engine, package, and score kind must be bounded identifiers")
	}
	packageFound := false
	for _, requirement := range packages {
		if scientificPackageRequirementName(requirement.Spec) == result.EnginePackage {
			packageFound = true
			break
		}
	}
	if !packageFound {
		return ScientificEvidenceRequest{}, errors.New("scientific evidence engine_package must be present in the managed package request")
	}
	var err error
	result.Inputs, err = normalizeScientificFileWitnesses(result.Inputs, "input")
	if err != nil {
		return ScientificEvidenceRequest{}, err
	}
	result.Artifacts, err = normalizeScientificFileWitnesses(result.Artifacts, "artifact")
	if err != nil {
		return ScientificEvidenceRequest{}, err
	}
	if len(result.CodePaths) > maxScientificFiles {
		return ScientificEvidenceRequest{}, errors.New("scientific code path count exceeds the bounded limit")
	}
	codePaths := make([]string, 0, len(result.CodePaths))
	seenCodePaths := make(map[string]struct{}, len(result.CodePaths))
	for index, raw := range result.CodePaths {
		path, pathErr := normalizeScientificRelativePath(raw)
		if pathErr != nil {
			return ScientificEvidenceRequest{}, fmt.Errorf("scientific code path %d is invalid", index)
		}
		if _, duplicate := seenCodePaths[path]; duplicate {
			return ScientificEvidenceRequest{}, fmt.Errorf("scientific code path %d duplicates an earlier path", index)
		}
		seenCodePaths[path] = struct{}{}
		codePaths = append(codePaths, path)
	}
	result.CodePaths = codePaths
	if result.WeightsPath != "" {
		result.WeightsPath, err = normalizeScientificRelativePath(result.WeightsPath)
		if err != nil {
			return ScientificEvidenceRequest{}, errors.New("scientific weights_path is invalid")
		}
	}
	return result, nil
}

func normalizeScientificFileWitnesses(
	values []ScientificFileWitness,
	label string,
) ([]ScientificFileWitness, error) {
	if len(values) == 0 || len(values) > maxScientificFiles {
		return nil, fmt.Errorf("scientific %s witness count is invalid", label)
	}
	result := make([]ScientificFileWitness, 0, len(values))
	seenKinds := make(map[string]struct{}, len(values))
	seenPaths := make(map[string]struct{}, len(values))
	for index, value := range values {
		value.Kind = strings.ToLower(strings.TrimSpace(value.Kind))
		path, err := normalizeScientificRelativePath(value.Path)
		if !scientificKind.MatchString(value.Kind) || err != nil {
			return nil, fmt.Errorf("scientific %s witness %d is invalid", label, index)
		}
		if _, duplicate := seenKinds[value.Kind]; duplicate {
			return nil, fmt.Errorf("scientific %s witness %d duplicates a kind", label, index)
		}
		if _, duplicate := seenPaths[path]; duplicate {
			return nil, fmt.Errorf("scientific %s witness %d duplicates a path", label, index)
		}
		seenKinds[value.Kind] = struct{}{}
		seenPaths[path] = struct{}{}
		value.Path = path
		result = append(result, value)
	}
	return result, nil
}

func normalizeScientificRelativePath(raw string) (string, error) {
	path := filepath.ToSlash(strings.TrimSpace(raw))
	clean := filepath.ToSlash(filepath.Clean(path))
	if path == "" || filepath.IsAbs(path) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "../") || strings.ContainsRune(path, '\x00') {
		return "", errors.New("scientific evidence path must be relative to working_dir")
	}
	return clean, nil
}

func scientificPackageRequirementName(spec string) string {
	value := strings.ToLower(strings.TrimSpace(spec))
	if bracket := strings.IndexByte(value, '['); bracket >= 0 {
		value = value[:bracket]
	}
	for index, current := range value {
		if current == ' ' || current == '=' || current == '!' || current == '~' || current == '>' || current == '<' {
			value = value[:index]
			break
		}
	}
	return strings.TrimSpace(value)
}

// ScientificEvidenceDigest binds the normalized semantic declaration to the
// runtime receipt without trusting a model-supplied digest.
func ScientificEvidenceDigest(input ScientificEvidenceRequest) (string, error) {
	normalized, err := normalizeScientificEvidence(input, []PackageRequirement{{Manager: PackageManagerConda, Spec: input.EnginePackage}})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func boundedPackageSpec(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func normalizeUniqueStrings(values []string, limit int, valid func(string) bool, label string) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("%s count exceeds the bounded limit", label)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if !valid(value) {
			return nil, fmt.Errorf("%s %d is invalid", label, index)
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s %d duplicates an earlier value", label, index)
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func RequestDigest(input Request) (string, error) {
	normalized, err := NormalizeRequest(input)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func EnvironmentName(providerID string, input Request) (string, error) {
	normalized, err := NormalizeRequest(input)
	if err != nil {
		return "", err
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if !requestIdentifier.MatchString(providerID) {
		return "", errors.New("software provider identity is invalid")
	}
	if normalized.SourceEnvironment != "" {
		return normalized.SourceEnvironment, nil
	}
	payload := struct {
		Provider string               `json:"provider"`
		Language string               `json:"language"`
		Packages []PackageRequirement `json:"packages"`
		Channels []string             `json:"channels"`
	}{providerID, normalized.Language, normalized.Packages, normalized.Channels}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "swr-" + hex.EncodeToString(digest[:12]), nil
}

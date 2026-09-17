package sciencecapability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	CatalogSchemaVersion = 2
	WitnessVersion       = 1
	maxCatalogBytes      = 256 << 10
	maxCapabilities      = 128
	maxEngines           = 32
	maxClosedIdentifiers = 64
	maxWitnessInputs     = 64
	maxWitnessArtifacts  = 64
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Catalog struct {
	SchemaVersion int          `json:"schemaVersion"`
	Capabilities  []Definition `json:"capabilities"`
}

type Definition struct {
	ID              string             `json:"id"`
	Description     string             `json:"description"`
	AcceptedEngines []EngineDefinition `json:"acceptedEngines"`
}

type EngineDefinition struct {
	ID                string        `json:"id"`
	Package           string        `json:"package,omitempty"`
	RequiredInputs    []string      `json:"requiredInputs"`
	ScoreKinds        []string      `json:"scoreKinds"`
	RequiredArtifacts []string      `json:"requiredArtifacts"`
	RequiresWeights   bool          `json:"requiresWeights"`
	ExecutionPack     ExecutionPack `json:"executionPack"`
}

type ExecutionPack struct {
	ID                string                      `json:"id"`
	Mode              string                      `json:"mode"`
	Skill             string                      `json:"skill"`
	Provider          string                      `json:"provider,omitempty"`
	Language          string                      `json:"language,omitempty"`
	Packages          []ExecutionPackage          `json:"packages,omitempty"`
	Channels          []string                    `json:"channels,omitempty"`
	Imports           []string                    `json:"imports,omitempty"`
	Executable        string                      `json:"executable,omitempty"`
	Script            string                      `json:"script,omitempty"`
	Modules           []string                    `json:"modules,omitempty"`
	CLIWitnesses      []ExecutionCLIWitness       `json:"cliWitnesses,omitempty"`
	Inputs            []ExecutionInput            `json:"inputs,omitempty"`
	Downloads         []ExecutionDownload         `json:"downloads,omitempty"`
	Parameters        []ExecutionParameter        `json:"parameters,omitempty"`
	EvidenceResolvers []ExecutionEvidenceResolver `json:"evidenceResolvers,omitempty"`
	Outputs           []ExecutionOutput           `json:"outputs,omitempty"`
	Comparisons       []ExecutionComparison       `json:"comparisons,omitempty"`
	ScoreKind         string                      `json:"scoreKind,omitempty"`
	UnavailableReason string                      `json:"unavailableReason,omitempty"`
}

type ExecutionPackage struct {
	Manager string `json:"manager"`
	Spec    string `json:"spec"`
}

type ExecutionCLIWitness struct {
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
}

type ExecutionInput struct {
	Kind       string   `json:"kind"`
	Argument   string   `json:"argument"`
	Extensions []string `json:"extensions"`
}

// ExecutionDownload binds one immutable, externally hosted input to a local
// execution pack. The catalog, not Skill prose or model-authored URLs, owns the
// source identity and checksum.
type ExecutionDownload struct {
	InputKind     string   `json:"inputKind"`
	URL           string   `json:"url"`
	Filename      string   `json:"filename"`
	SHA256        string   `json:"sha256"`
	SizeBytes     int64    `json:"sizeBytes"`
	RedirectHosts []string `json:"redirectHosts,omitempty"`
}

type ExecutionParameter struct {
	Name          string   `json:"name"`
	Argument      string   `json:"argument"`
	Type          string   `json:"type"`
	Required      bool     `json:"required"`
	Default       any      `json:"default,omitempty"`
	Minimum       *float64 `json:"minimum,omitempty"`
	Maximum       *float64 `json:"maximum,omitempty"`
	Evidence      string   `json:"evidence,omitempty"`
	EvidenceGroup string   `json:"evidenceGroup,omitempty"`
	EvidenceTerms []string `json:"evidenceTerms,omitempty"`
}

type ExecutionEvidenceResolver struct {
	EvidenceGroup  string `json:"evidenceGroup"`
	Skill          string `json:"skill"`
	Implementation string `json:"implementation"`
}

type ExecutionOutput struct {
	Kind             string   `json:"kind"`
	Path             string   `json:"path"`
	Format           string   `json:"format"`
	Delivery         string   `json:"delivery,omitempty"`
	MinBytes         int64    `json:"minBytes"`
	MinRecords       int      `json:"minRecords,omitempty"`
	RequiredJSONTrue []string `json:"requiredJsonTrue,omitempty"`
}

type ExecutionComparison struct {
	Path           string   `json:"path"`
	DerivedColumns []string `json:"derivedColumns"`
	BasisColumns   []string `json:"basisColumns"`
	GroupColumns   []string `json:"groupColumns,omitempty"`
}

// Witness is emitted by a trusted compute adapter after the external process
// has terminated and its outputs have been harvested. It is deliberately
// independent from model prose and skill instructions.
type Witness struct {
	Version           int               `json:"version"`
	Capability        string            `json:"capability"`
	Engine            string            `json:"engine"`
	EnginePackage     string            `json:"enginePackage"`
	EngineVersion     string            `json:"engineVersion"`
	ProfileSHA256     string            `json:"profileSha256"`
	CodeSHA256        string            `json:"codeSha256"`
	WeightsSHA256     string            `json:"weightsSha256,omitempty"`
	EnvironmentSHA256 string            `json:"environmentSha256"`
	Provider          string            `json:"provider"`
	JobID             string            `json:"jobId"`
	State             string            `json:"state"`
	ScoreKind         string            `json:"scoreKind"`
	Inputs            map[string]string `json:"inputs"`
	Artifacts         []ArtifactWitness `json:"artifacts"`
	CompletedAt       time.Time         `json:"completedAt"`
}

type ArtifactWitness struct {
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

type Evaluation struct {
	Satisfied []string
	Missing   []string
	Invalid   []string
}

func Load(path string) (Catalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("open scientific capability catalog: %w", err)
	}
	defer file.Close()
	return Decode(io.LimitReader(file, maxCatalogBytes+1))
}

func Decode(reader io.Reader) (Catalog, error) {
	if reader == nil {
		return Catalog{}, errors.New("scientific capability catalog reader is required")
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return Catalog{}, fmt.Errorf("read scientific capability catalog: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxCatalogBytes {
		return Catalog{}, errors.New("scientific capability catalog size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode scientific capability catalog: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Catalog{}, err
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (catalog Catalog) Validate() error {
	if catalog.SchemaVersion != CatalogSchemaVersion {
		return fmt.Errorf("unsupported scientific capability catalog schema version %d", catalog.SchemaVersion)
	}
	if len(catalog.Capabilities) == 0 || len(catalog.Capabilities) > maxCapabilities {
		return errors.New("scientific capability catalog is empty")
	}
	capabilities := make(map[string]struct{}, len(catalog.Capabilities))
	localResolverTargets := map[string]map[string]bool{}
	for _, capability := range catalog.Capabilities {
		if !identifierPattern.MatchString(capability.ID) || !validBoundedText(capability.Description, 1024) {
			return fmt.Errorf("scientific capability %q is invalid", capability.ID)
		}
		if _, exists := capabilities[capability.ID]; exists {
			return fmt.Errorf("duplicate scientific capability %q", capability.ID)
		}
		capabilities[capability.ID] = struct{}{}
		if len(capability.AcceptedEngines) == 0 || len(capability.AcceptedEngines) > maxEngines {
			return fmt.Errorf("scientific capability %q has no accepted engine", capability.ID)
		}
		engines := map[string]struct{}{}
		for _, engine := range capability.AcceptedEngines {
			if !identifierPattern.MatchString(engine.ID) {
				return fmt.Errorf("scientific capability %q engine %q is invalid", capability.ID, engine.ID)
			}
			if _, exists := engines[engine.ID]; exists {
				return fmt.Errorf("scientific capability %q has duplicate engine %q", capability.ID, engine.ID)
			}
			engines[engine.ID] = struct{}{}
			if engine.Package != "" && !identifierPattern.MatchString(engine.Package) {
				return fmt.Errorf("scientific capability %q engine %q package %q is invalid", capability.ID, engine.ID, engine.Package)
			}
			if err := validateClosedIdentifiers(engine.RequiredInputs, "input kind"); err != nil {
				return fmt.Errorf("scientific capability %q engine %q: %w", capability.ID, engine.ID, err)
			}
			if err := validateClosedIdentifiers(engine.ScoreKinds, "score kind"); err != nil {
				return fmt.Errorf("scientific capability %q engine %q: %w", capability.ID, engine.ID, err)
			}
			if err := validateClosedIdentifiers(engine.RequiredArtifacts, "artifact kind"); err != nil {
				return fmt.Errorf("scientific capability %q engine %q: %w", capability.ID, engine.ID, err)
			}
			if err := validateExecutionPack(capability.ID, engine); err != nil {
				return fmt.Errorf("scientific capability %q engine %q execution pack: %w", capability.ID, engine.ID, err)
			}
			if engine.ExecutionPack.Mode == "local" {
				skill := strings.ToLower(strings.TrimSpace(engine.ExecutionPack.Skill))
				if localResolverTargets[skill] == nil {
					localResolverTargets[skill] = map[string]bool{}
				}
				localResolverTargets[skill][strings.ToLower(strings.TrimSpace(engine.ID))] = true
				localResolverTargets[skill][strings.ToLower(strings.TrimSpace(engine.Package))] = true
			}
		}
	}
	for _, capability := range catalog.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			for _, resolver := range engine.ExecutionPack.EvidenceResolvers {
				targets := localResolverTargets[strings.ToLower(strings.TrimSpace(resolver.Skill))]
				if len(targets) == 0 || !targets[strings.ToLower(strings.TrimSpace(resolver.Implementation))] {
					return fmt.Errorf("scientific capability %q engine %q execution evidence resolver target is unavailable", capability.ID, engine.ID)
				}
			}
		}
	}
	return nil
}

func (catalog Catalog) FindExecutionPack(id string) (Definition, EngineDefinition, bool) {
	id = strings.TrimSpace(id)
	for _, capability := range catalog.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			if engine.ExecutionPack.ID == id {
				return capability, engine, true
			}
		}
	}
	return Definition{}, EngineDefinition{}, false
}

// LocalExecutionPacksForSkill returns the machine-reviewed execution packs
// owned by one loaded Skill. A registered local pack is the canonical runtime
// boundary for its engine: model-authored commands may prepare unrelated data,
// but they must not invoke the pack's engine outside that entrypoint.
func (catalog Catalog) LocalExecutionPacksForSkill(skillName string) []EngineDefinition {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return nil
	}
	result := make([]EngineDefinition, 0)
	for _, capability := range catalog.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			if engine.ExecutionPack.Mode == "local" && strings.EqualFold(engine.ExecutionPack.Skill, skillName) {
				result = append(result, engine)
			}
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ExecutionPack.ID < result[right].ExecutionPack.ID
	})
	return result
}

// ManagedExecutionIdentifiers derives the implementation-owned process and
// import names from the closed execution-pack registry. It deliberately does
// not read Skill prose or task text.
func (engine EngineDefinition) ManagedExecutionIdentifiers() []string {
	values := []string{engine.ID, engine.Package}
	for _, witness := range engine.ExecutionPack.CLIWitnesses {
		values = append(values, witness.Executable)
	}
	for _, importName := range engine.ExecutionPack.Imports {
		values = append(values, importName)
		if separator := strings.IndexByte(importName, '.'); separator > 0 {
			values = append(values, importName[:separator])
		}
	}
	for index := range values {
		values[index] = strings.ToLower(strings.TrimSpace(values[index]))
	}
	return uniqueSorted(values)
}

// MaterializedSkillEntrypoint maps an embedded execution-pack script to the
// corresponding immutable asset in the task's materialized Skill bundle.
func (pack ExecutionPack) MaterializedSkillEntrypoint() string {
	if pack.Mode != "local" || !safeExecutionPackPath(pack.Script) {
		return ""
	}
	return filepath.ToSlash(filepath.Join("scripts", filepath.Base(pack.Script)))
}

func validateExecutionPack(capabilityID string, engine EngineDefinition) error {
	pack := engine.ExecutionPack
	if pack.ID != capabilityID+"."+engine.ID || !identifierPattern.MatchString(pack.Skill) {
		return errors.New("id or skill is invalid")
	}
	switch pack.Mode {
	case "unavailable":
		if !validBoundedText(pack.UnavailableReason, 1024) || pack.Provider != "" || pack.Language != "" ||
			len(pack.Packages) != 0 || pack.Executable != "" || pack.Script != "" || len(pack.Modules) != 0 || len(pack.Inputs) != 0 ||
			len(pack.EvidenceResolvers) != 0 || len(pack.Outputs) != 0 || len(pack.Comparisons) != 0 {
			return errors.New("unavailable pack must contain only its reason and discovery identity")
		}
		return nil
	case "local":
	default:
		return fmt.Errorf("unsupported mode %q", pack.Mode)
	}
	if pack.UnavailableReason != "" || pack.Provider != "local-conda" ||
		(pack.Language != "python" && pack.Language != "r" && pack.Language != "native") ||
		!pathFreeExecutable(pack.Executable) || !safeExecutionPackPath(pack.Script) {
		return errors.New("local runtime identity is invalid")
	}
	if len(pack.Modules) > 16 || len(pack.Modules) > 0 && pack.Language != "python" {
		return errors.New("local execution pack module set is invalid")
	}
	modulePaths := map[string]bool{filepath.ToSlash(strings.TrimSpace(pack.Script)): true}
	for _, module := range pack.Modules {
		module = filepath.ToSlash(strings.TrimSpace(module))
		if !safeExecutionPackPath(module) || strings.ToLower(filepath.Ext(module)) != ".py" || modulePaths[module] {
			return errors.New("local execution pack module path is invalid or duplicated")
		}
		modulePaths[module] = true
	}
	if len(pack.Packages) == 0 || len(pack.Packages) > 64 || len(pack.CLIWitnesses) == 0 || len(pack.CLIWitnesses) > 16 {
		return errors.New("local package or CLI witness set is invalid")
	}
	packages := map[string]bool{}
	for _, item := range pack.Packages {
		key := strings.ToLower(strings.TrimSpace(item.Manager)) + "\x00" + strings.TrimSpace(item.Spec)
		if (item.Manager != "conda" && item.Manager != "pip") || !validBoundedText(item.Spec, 256) || packages[key] {
			return errors.New("package declaration is invalid or duplicated")
		}
		packages[key] = true
	}
	if err := validateBoundedUniqueStrings(pack.Channels, 16, 128); err != nil {
		return fmt.Errorf("channels: %w", err)
	}
	if err := validateBoundedUniqueStrings(pack.Imports, 32, 128); err != nil {
		return fmt.Errorf("imports: %w", err)
	}
	for _, witness := range pack.CLIWitnesses {
		if !pathFreeExecutable(witness.Executable) || len(witness.Arguments) == 0 || len(witness.Arguments) > 16 {
			return errors.New("CLI witness is invalid")
		}
		for _, argument := range witness.Arguments {
			if !validBoundedText(argument, 256) {
				return errors.New("CLI witness argument is invalid")
			}
		}
	}
	inputKinds := map[string]bool{}
	for _, input := range pack.Inputs {
		if !contains(engine.RequiredInputs, input.Kind) || inputKinds[input.Kind] || !validExecutionFlag(input.Argument) ||
			len(input.Extensions) == 0 || len(input.Extensions) > 16 {
			return errors.New("input binding is invalid or duplicated")
		}
		for _, extension := range input.Extensions {
			if len(extension) < 2 || len(extension) > 16 || extension[0] != '.' || strings.ToLower(extension) != extension {
				return errors.New("input extension is invalid")
			}
		}
		inputKinds[input.Kind] = true
	}
	for _, required := range engine.RequiredInputs {
		if !inputKinds[required] {
			return errors.New("missing input binding " + required)
		}
	}
	if len(pack.Downloads) > 16 {
		return errors.New("execution download set is invalid")
	}
	seenDownloads := map[string]bool{}
	for _, download := range pack.Downloads {
		parsed, parseErr := url.Parse(strings.TrimSpace(download.URL))
		filename := strings.TrimSpace(download.Filename)
		key := strings.ToLower(strings.TrimSpace(download.InputKind) + "\x00" + strings.TrimSpace(download.URL))
		validExtension := false
		for _, input := range pack.Inputs {
			if input.Kind != download.InputKind {
				continue
			}
			for _, extension := range input.Extensions {
				validExtension = validExtension || strings.HasSuffix(strings.ToLower(filename), extension)
			}
		}
		if !inputKinds[download.InputKind] || parseErr != nil || parsed.Scheme != "https" ||
			parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(download.URL) > 2048 ||
			filename == "" || filename != filepath.Base(filename) || len(filename) > 255 ||
			!digestPattern.MatchString(download.SHA256) || download.SizeBytes <= 0 ||
			download.SizeBytes > 1<<40 || !validExtension || seenDownloads[key] {
			return errors.New("execution download is invalid or duplicated")
		}
		if len(download.RedirectHosts) > 8 {
			return errors.New("execution download redirect host set is invalid")
		}
		seenRedirectHosts := map[string]bool{}
		for _, rawHost := range download.RedirectHosts {
			host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawHost), "."))
			if host == "" || len(host) > 253 || strings.ContainsAny(host, "/:@") ||
				seenRedirectHosts[host] {
				return errors.New("execution download redirect host is invalid or duplicated")
			}
			seenRedirectHosts[host] = true
		}
		seenDownloads[key] = true
	}
	parameterNames, parameterArguments := map[string]bool{}, map[string]bool{}
	evidenceGroupTerms := map[string]bool{}
	for _, parameter := range pack.Parameters {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(parameter.Name) || parameterNames[parameter.Name] ||
			!validExecutionFlag(parameter.Argument) || parameterArguments[parameter.Argument] ||
			(parameter.Type != "number" && parameter.Type != "integer" && parameter.Type != "string" && parameter.Type != "boolean") ||
			parameter.Minimum != nil && parameter.Maximum != nil && *parameter.Minimum > *parameter.Maximum ||
			(parameter.Evidence != "" && parameter.Evidence != "resolved-user-input" &&
				parameter.Evidence != "runtime-response-language" && parameter.Evidence != "selected-evidence-resolver") ||
			(parameter.Evidence == "resolved-user-input" && (parameter.EvidenceGroup == "" ||
				(parameter.Type != "number" && parameter.Type != "integer" && parameter.Type != "string"))) ||
			(parameter.Evidence == "runtime-response-language" &&
				(parameter.Type != "string" || parameter.EvidenceGroup != "" || len(parameter.EvidenceTerms) != 0)) ||
			(parameter.Evidence == "selected-evidence-resolver" &&
				(parameter.Type != "string" || parameter.EvidenceGroup != "" || len(parameter.EvidenceTerms) != 0)) ||
			(parameter.EvidenceGroup != "" &&
				(parameter.Evidence != "resolved-user-input" || !identifierPattern.MatchString(parameter.EvidenceGroup))) ||
			(len(parameter.EvidenceTerms) > 0 && (parameter.EvidenceGroup == "" || len(parameter.EvidenceTerms) > 16)) {
			return errors.New("parameter binding is invalid or duplicated")
		}
		seenEvidenceTerms := map[string]bool{}
		for _, term := range parameter.EvidenceTerms {
			term = strings.TrimSpace(term)
			key := strings.ToLower(term)
			if !validBoundedText(term, 80) || seenEvidenceTerms[key] {
				return errors.New("parameter evidence term is invalid or duplicated")
			}
			seenEvidenceTerms[key] = true
		}
		if parameter.EvidenceGroup != "" {
			evidenceGroupTerms[parameter.EvidenceGroup] = evidenceGroupTerms[parameter.EvidenceGroup] || len(parameter.EvidenceTerms) > 0
		}
		if parameter.Default != nil && !executionParameterValueValid(parameter, parameter.Default) {
			return errors.New("parameter default is invalid")
		}
		parameterNames[parameter.Name], parameterArguments[parameter.Argument] = true, true
	}
	for _, hasTerms := range evidenceGroupTerms {
		if !hasTerms {
			return errors.New("resolved-user-input evidence group requires registry evidence terms")
		}
	}
	if len(pack.EvidenceResolvers) > 16 {
		return errors.New("execution evidence resolver set is invalid")
	}
	seenResolvers := map[string]bool{}
	for _, resolver := range pack.EvidenceResolvers {
		key := strings.ToLower(strings.TrimSpace(resolver.EvidenceGroup) + "\x00" + strings.TrimSpace(resolver.Skill) + "\x00" + strings.TrimSpace(resolver.Implementation))
		if !evidenceGroupTerms[resolver.EvidenceGroup] || !identifierPattern.MatchString(resolver.Skill) ||
			!validBoundedText(resolver.Implementation, 128) || seenResolvers[key] {
			return errors.New("execution evidence resolver is invalid or duplicated")
		}
		seenResolvers[key] = true
	}
	if !contains(engine.ScoreKinds, pack.ScoreKind) {
		return errors.New("score kind is not accepted by the engine")
	}
	outputKinds := map[string]bool{}
	for _, output := range pack.Outputs {
		if !identifierPattern.MatchString(output.Kind) || outputKinds[output.Kind] || !safeExecutionPackPath(output.Path) ||
			!contains([]string{"json", "csv", "tsv", "xyz", "png", "html", "text", "binary"}, output.Format) ||
			!contains([]string{"", "snapshot", "working_data"}, output.Delivery) ||
			output.MinBytes < 0 || output.MinRecords < 0 {
			return errors.New("output binding is invalid or duplicated")
		}
		for _, pointer := range output.RequiredJSONTrue {
			if !strings.HasPrefix(pointer, "/") || !validBoundedText(pointer, 512) {
				return errors.New("output JSON assertion is invalid")
			}
		}
		outputKinds[output.Kind] = true
	}
	for _, required := range engine.RequiredArtifacts {
		if !outputKinds[required] {
			return errors.New("missing output binding " + required)
		}
	}
	outputPaths := map[string]bool{}
	for _, output := range pack.Outputs {
		outputPaths[output.Path] = true
	}
	for _, comparison := range pack.Comparisons {
		if !outputPaths[comparison.Path] || len(comparison.DerivedColumns) == 0 || len(comparison.BasisColumns) == 0 ||
			validateBoundedUniqueStrings(comparison.DerivedColumns, 128, 128) != nil ||
			validateBoundedUniqueStrings(comparison.BasisColumns, 128, 128) != nil ||
			validateBoundedUniqueStrings(comparison.GroupColumns, 128, 128) != nil {
			return errors.New("comparison contract is invalid")
		}
	}
	return nil
}

func validateBoundedUniqueStrings(values []string, limit, textLimit int) error {
	if len(values) > limit {
		return errors.New("too many values")
	}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validBoundedText(value, textLimit) || seen[value] {
			return errors.New("value is invalid or duplicated")
		}
		seen[value] = true
	}
	return nil
}

func pathFreeExecutable(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && filepath.Base(value) == value && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func safeExecutionPackPath(value string) bool {
	value = filepath.ToSlash(strings.TrimSpace(value))
	clean := filepath.ToSlash(filepath.Clean(value))
	return value != "" && value == clean && clean != "." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, "/")
}

func validExecutionFlag(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "--") && len(value) > 2 && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n")
}

func executionParameterValueValid(parameter ExecutionParameter, value any) bool {
	var numeric float64
	switch parameter.Type {
	case "number":
		switch typed := value.(type) {
		case float64:
			numeric = typed
		case int:
			numeric = float64(typed)
		case int64:
			numeric = float64(typed)
		default:
			return false
		}
	case "integer":
		switch typed := value.(type) {
		case float64:
			if typed != float64(int64(typed)) {
				return false
			}
			numeric = typed
		case int:
			numeric = float64(typed)
		case int64:
			numeric = float64(typed)
		default:
			return false
		}
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
	return (parameter.Minimum == nil || numeric >= *parameter.Minimum) &&
		(parameter.Maximum == nil || numeric <= *parameter.Maximum)
}

func Evaluate(catalog Catalog, required []string, witnesses []Witness) Evaluation {
	definitions := make(map[string]Definition, len(catalog.Capabilities))
	for _, definition := range catalog.Capabilities {
		definitions[definition.ID] = definition
	}
	required = uniqueSorted(required)
	evaluation := Evaluation{}
	for _, capability := range required {
		definition, known := definitions[capability]
		if !known {
			evaluation.Invalid = append(evaluation.Invalid, capability+":unknown_capability")
			continue
		}
		valid := false
		for _, witness := range witnesses {
			if witness.Capability != capability {
				continue
			}
			if err := validateWitness(definition, witness); err != nil {
				evaluation.Invalid = append(evaluation.Invalid, capability+":"+err.Error())
				continue
			}
			valid = true
			break
		}
		if valid {
			evaluation.Satisfied = append(evaluation.Satisfied, capability)
		} else {
			evaluation.Missing = append(evaluation.Missing, capability)
		}
	}
	sort.Strings(evaluation.Invalid)
	return evaluation
}

func ValidateWitness(catalog Catalog, witness Witness) error {
	for _, definition := range catalog.Capabilities {
		if definition.ID == witness.Capability {
			return validateWitness(definition, witness)
		}
	}
	return errors.New("unknown_capability")
}

func validateWitness(definition Definition, witness Witness) error {
	if witness.Version != WitnessVersion || witness.Capability != definition.ID {
		return errors.New("invalid_contract")
	}
	var engine *EngineDefinition
	for index := range definition.AcceptedEngines {
		if definition.AcceptedEngines[index].ID == witness.Engine {
			engine = &definition.AcceptedEngines[index]
			break
		}
	}
	if engine == nil {
		return errors.New("unapproved_engine")
	}
	if !identifierPattern.MatchString(witness.EnginePackage) ||
		(engine.Package != "" && witness.EnginePackage != engine.Package) {
		return errors.New("unapproved_engine_package")
	}
	if !validBoundedText(witness.EngineVersion, 128) || !validBoundedText(witness.Provider, 128) || !validBoundedText(witness.JobID, 256) {
		return errors.New("missing_identity")
	}
	if witness.State != "completed" || witness.CompletedAt.IsZero() {
		return errors.New("not_completed")
	}
	if witness.CompletedAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		return errors.New("invalid_completed_at")
	}
	for _, digest := range []string{witness.ProfileSHA256, witness.CodeSHA256, witness.EnvironmentSHA256} {
		if !digestPattern.MatchString(digest) {
			return errors.New("invalid_digest")
		}
	}
	if engine.RequiresWeights && !digestPattern.MatchString(witness.WeightsSHA256) {
		return errors.New("invalid_weights_digest")
	}
	if witness.WeightsSHA256 != "" && !digestPattern.MatchString(witness.WeightsSHA256) {
		return errors.New("invalid_weights_digest")
	}
	if len(witness.Inputs) == 0 || len(witness.Inputs) > maxWitnessInputs {
		return errors.New("missing_inputs")
	}
	for key, digest := range witness.Inputs {
		if !identifierPattern.MatchString(key) || !digestPattern.MatchString(digest) {
			return errors.New("invalid_input_digest")
		}
	}
	for _, required := range engine.RequiredInputs {
		if _, exists := witness.Inputs[required]; !exists {
			return errors.New("missing_input_" + required)
		}
	}
	if !contains(engine.ScoreKinds, witness.ScoreKind) {
		return errors.New("invalid_score_kind")
	}
	if len(witness.Artifacts) == 0 || len(witness.Artifacts) > maxWitnessArtifacts {
		return errors.New("invalid_artifact")
	}
	artifacts := map[string]bool{}
	for _, artifact := range witness.Artifacts {
		if !identifierPattern.MatchString(artifact.Kind) || !digestPattern.MatchString(artifact.SHA256) {
			return errors.New("invalid_artifact")
		}
		if artifacts[artifact.Kind] {
			return errors.New("duplicate_artifact")
		}
		artifacts[artifact.Kind] = true
	}
	for _, required := range engine.RequiredArtifacts {
		if !artifacts[required] {
			return errors.New("missing_artifact_" + required)
		}
	}
	return nil
}

func validateClosedIdentifiers(values []string, label string) error {
	if len(values) == 0 || len(values) > maxClosedIdentifiers {
		return fmt.Errorf("%s list is empty", label)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("invalid %s %q", label, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validBoundedText(value string, max int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return false
	}
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			return false
		}
	}
	return true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("scientific capability catalog must contain one JSON value")
		}
		return fmt.Errorf("decode scientific capability catalog trailing data: %w", err)
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

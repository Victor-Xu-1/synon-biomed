package oracle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"synon-go/internal/compat/contracts"
)

var (
	ErrIncomplete      = errors.New("behavioral contract evidence is incomplete")
	ErrInvalidEvidence = errors.New("invalid behavioral contract evidence")
)

type ContractKind string

const (
	ServiceContract   ContractKind = "service"
	HTTPRouteContract ContractKind = "http-route"
	EventContract     ContractKind = "event"
	QueryContract     ContractKind = "query"
)

type EvidenceStatus string

const (
	MissingStatus       EvidenceStatus = "missing"
	ImplementedReal     EvidenceStatus = "implemented-real"
	DifferentialPass    EvidenceStatus = "differential-pass"
	ExternalPass        EvidenceStatus = "external-pass"
	FrontendOwnedStatus EvidenceStatus = "frontend-owned"
	BlockedExternal     EvidenceStatus = "blocked-external"
)

type CaptureRef struct {
	Artifact   string `json:"artifact"`
	SHA256     string `json:"sha256"`
	Runtime    string `json:"runtime"`
	CapturedAt string `json:"capturedAt"`
}

type ProbeEvidence struct {
	ID               string         `json:"id"`
	Scenario         string         `json:"scenario"`
	Fixture          string         `json:"fixture"`
	Transport        string         `json:"transport"`
	Trigger          string         `json:"trigger"`
	Producer         string         `json:"producer,omitempty"`
	Delivery         string         `json:"delivery,omitempty"`
	Invalidation     string         `json:"invalidation,omitempty"`
	ComparisonMode   ComparisonMode `json:"comparisonMode,omitempty"`
	Normalization    []string       `json:"normalization"`
	Assertions       []string       `json:"assertions"`
	SourceFiles      []string       `json:"sourceFiles"`
	TestCommand      string         `json:"testCommand"`
	BaselineCapture  CaptureRef     `json:"baselineCapture"`
	CandidateCapture CaptureRef     `json:"candidateCapture"`
}

type Record struct {
	Kind       ContractKind   `json:"kind"`
	Name       string         `json:"name"`
	Status     EvidenceStatus `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	SourceLine int            `json:"sourceLine,omitempty"`
	Probe      *ProbeEvidence `json:"probe,omitempty"`
}

type BaselineSummary struct {
	Services   int `json:"services"`
	HTTPRoutes int `json:"httpRoutes"`
	Events     int `json:"events"`
	Queries    int `json:"queries"`
}

type Manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Baseline      string          `json:"baseline"`
	Summary       BaselineSummary `json:"summary"`
	Records       []Record        `json:"records"`
}

type KindReport struct {
	Total         int `json:"total"`
	Evidenced     int `json:"evidenced"`
	Missing       int `json:"missing"`
	FrontendOwned int `json:"frontendOwned"`
}

type Report struct {
	Valid         bool                        `json:"valid"`
	Total         int                         `json:"total"`
	InScope       int                         `json:"inScope"`
	Evidenced     int                         `json:"evidenced"`
	Missing       int                         `json:"missing"`
	FrontendOwned int                         `json:"frontendOwned"`
	Blocked       int                         `json:"blocked"`
	ByKind        map[ContractKind]KindReport `json:"byKind"`
}

func Scaffold() Manifest {
	summary := contracts.V11Summary()
	records := make([]Record, 0, summary.ServiceMethodCount+summary.HTTPRouteCount+summary.EventTypeCount+summary.QueryKeyCount)
	for _, service := range contracts.ServiceMethods {
		record := Record{
			Kind:       ServiceContract,
			Name:       service.Method,
			Status:     MissingStatus,
			SourceLine: service.SourceLine,
		}
		if frontendOnlyService(service.Method) {
			record.Status = FrontendOwnedStatus
			if service.Method == "getBench" {
				record.Reason = "client-only in-memory bench selector; frontend state implementation is owned by the separate frontend project"
			} else {
				record.Reason = "client-only preflight helper; visual frontend implementation is owned by the separate frontend project"
			}
		}
		records = append(records, record)
	}
	for _, route := range contracts.HTTPRoutes {
		records = append(records, Record{
			Kind:       HTTPRouteContract,
			Name:       route.Name(),
			Status:     MissingStatus,
			SourceLine: route.SourceLine,
		})
	}
	for _, event := range contracts.EventTypes {
		records = append(records, Record{
			Kind:       EventContract,
			Name:       event.Name,
			Status:     MissingStatus,
			SourceLine: event.SourceLine,
		})
	}
	for _, query := range contracts.QueryKeys {
		records = append(records, Record{
			Kind:       QueryContract,
			Name:       query.Name,
			Status:     MissingStatus,
			SourceLine: query.SourceLine,
		})
	}
	return Manifest{
		SchemaVersion: 2,
		Baseline:      "synonbiomed-v1.1",
		Summary: BaselineSummary{
			Services:   summary.ServiceMethodCount,
			HTTPRoutes: summary.HTTPRouteCount,
			Events:     summary.EventTypeCount,
			Queries:    summary.QueryKeyCount,
		},
		Records: records,
	}
}

func Verify(evidenceRoot string, manifest Manifest) (Report, error) {
	expected := expectedContracts()
	report := Report{
		Total:  len(expected),
		ByKind: map[ContractKind]KindReport{},
	}
	seen := make(map[string]struct{}, len(manifest.Records))
	probeIDs := make(map[string]string)

	if manifest.SchemaVersion != 2 {
		return report, fmt.Errorf("%w: schemaVersion=%d, want 2", ErrInvalidEvidence, manifest.SchemaVersion)
	}
	if manifest.Baseline != "synonbiomed-v1.1" {
		return report, fmt.Errorf("%w: baseline=%q", ErrInvalidEvidence, manifest.Baseline)
	}
	expectedSummary := contracts.V11Summary()
	if manifest.Summary.Services != expectedSummary.ServiceMethodCount ||
		manifest.Summary.HTTPRoutes != expectedSummary.HTTPRouteCount ||
		manifest.Summary.Events != expectedSummary.EventTypeCount ||
		manifest.Summary.Queries != expectedSummary.QueryKeyCount {
		return report, fmt.Errorf("%w: baseline summary does not match recovered contracts", ErrInvalidEvidence)
	}

	for _, record := range manifest.Records {
		key := contractKey(record.Kind, record.Name)
		expectedRecord, exists := expected[key]
		if !exists {
			return report, fmt.Errorf("%w: unknown %s contract %q", ErrInvalidEvidence, record.Kind, record.Name)
		}
		if _, duplicate := seen[key]; duplicate {
			return report, fmt.Errorf("%w: duplicate %s contract %q", ErrInvalidEvidence, record.Kind, record.Name)
		}
		seen[key] = struct{}{}
		if record.SourceLine != expectedRecord.SourceLine {
			return report, fmt.Errorf(
				"%w: %s contract %q sourceLine=%d, want %d",
				ErrInvalidEvidence,
				record.Kind,
				record.Name,
				record.SourceLine,
				expectedRecord.SourceLine,
			)
		}

		kindReport := report.ByKind[record.Kind]
		kindReport.Total++
		switch record.Status {
		case ImplementedReal, DifferentialPass, ExternalPass:
			if err := validateProbe(evidenceRoot, record, probeIDs); err != nil {
				return report, err
			}
			kindReport.Evidenced++
			report.Evidenced++
			report.InScope++
		case FrontendOwnedStatus:
			if !frontendOnlyService(record.Name) || record.Kind != ServiceContract {
				return report, fmt.Errorf(
					"%w: %s contract %q cannot be frontend-owned",
					ErrInvalidEvidence,
					record.Kind,
					record.Name,
				)
			}
			if strings.TrimSpace(record.Reason) == "" || record.Probe != nil {
				return report, fmt.Errorf("%w: frontend-owned contract %q has invalid disposition", ErrInvalidEvidence, record.Name)
			}
			kindReport.FrontendOwned++
			report.FrontendOwned++
		case MissingStatus:
			if record.Probe != nil {
				return report, fmt.Errorf("%w: missing contract %q cannot contain passing probe evidence", ErrInvalidEvidence, record.Name)
			}
			kindReport.Missing++
			report.Missing++
			report.InScope++
		case BlockedExternal:
			if strings.TrimSpace(record.Reason) == "" || record.Probe != nil {
				return report, fmt.Errorf("%w: blocked contract %q requires a reason and no passing probe", ErrInvalidEvidence, record.Name)
			}
			kindReport.Missing++
			report.Missing++
			report.Blocked++
			report.InScope++
		default:
			return report, fmt.Errorf(
				"%w: %s contract %q has unknown status %q",
				ErrInvalidEvidence,
				record.Kind,
				record.Name,
				record.Status,
			)
		}
		report.ByKind[record.Kind] = kindReport
	}

	if len(seen) != len(expected) {
		missing := make([]string, 0, len(expected)-len(seen))
		for key := range expected {
			if _, exists := seen[key]; !exists {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		return report, fmt.Errorf("%w: manifest omits contracts: %s", ErrInvalidEvidence, strings.Join(missing, ", "))
	}
	if report.FrontendOwned != 3 || report.InScope != 328 {
		return report, fmt.Errorf(
			"%w: dispositions yield inScope=%d frontendOwned=%d, want 328 and 3",
			ErrInvalidEvidence,
			report.InScope,
			report.FrontendOwned,
		)
	}
	if report.Missing > 0 {
		return report, fmt.Errorf("%w: %d of %d in-scope contracts lack real evidence", ErrIncomplete, report.Missing, report.InScope)
	}
	report.Valid = true
	return report, nil
}

func Marshal(manifest Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal behavioral evidence: %w", err)
	}
	return append(data, '\n'), nil
}

func Unmarshal(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode behavioral evidence: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.New("decode behavioral evidence: trailing JSON content")
	}
	return manifest, nil
}

func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read behavioral evidence: %w", err)
	}
	return Unmarshal(data)
}

type expectedContract struct {
	Kind       ContractKind
	Name       string
	SourceLine int
}

func expectedContracts() map[string]expectedContract {
	expected := make(map[string]expectedContract, 331)
	for _, service := range contracts.ServiceMethods {
		item := expectedContract{Kind: ServiceContract, Name: service.Method, SourceLine: service.SourceLine}
		expected[contractKey(item.Kind, item.Name)] = item
	}
	for _, route := range contracts.HTTPRoutes {
		item := expectedContract{Kind: HTTPRouteContract, Name: route.Name(), SourceLine: route.SourceLine}
		expected[contractKey(item.Kind, item.Name)] = item
	}
	for _, event := range contracts.EventTypes {
		item := expectedContract{Kind: EventContract, Name: event.Name, SourceLine: event.SourceLine}
		expected[contractKey(item.Kind, item.Name)] = item
	}
	for _, query := range contracts.QueryKeys {
		item := expectedContract{Kind: QueryContract, Name: query.Name, SourceLine: query.SourceLine}
		expected[contractKey(item.Kind, item.Name)] = item
	}
	return expected
}

func contractKey(kind ContractKind, name string) string {
	return string(kind) + ":" + name
}

func frontendOnlyService(name string) bool {
	return name == "anchorPreflight" || name == "anchorAuthPreflight" || name == "getBench"
}

func validateProbe(evidenceRoot string, record Record, probeIDs map[string]string) error {
	probe := record.Probe
	if probe == nil {
		return fmt.Errorf("%w: %s contract %q has no probe", ErrInvalidEvidence, record.Kind, record.Name)
	}
	required := map[string]string{
		"id":          probe.ID,
		"scenario":    probe.Scenario,
		"fixture":     probe.Fixture,
		"transport":   probe.Transport,
		"trigger":     probe.Trigger,
		"testCommand": probe.TestCommand,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s contract %q probe has no %s", ErrInvalidEvidence, record.Kind, record.Name, field)
		}
	}
	if owner, exists := probeIDs[probe.ID]; exists {
		return fmt.Errorf("%w: probe id %q is shared by %s and %s", ErrInvalidEvidence, probe.ID, owner, contractKey(record.Kind, record.Name))
	}
	probeIDs[probe.ID] = contractKey(record.Kind, record.Name)
	if len(nonEmpty(probe.Assertions)) == 0 || len(nonEmpty(probe.SourceFiles)) == 0 {
		return fmt.Errorf("%w: %s contract %q probe requires assertions and sourceFiles", ErrInvalidEvidence, record.Kind, record.Name)
	}
	for _, sourceFile := range probe.SourceFiles {
		if err := validateEvidenceRelativePath(sourceFile); err != nil {
			return fmt.Errorf("%w: %s contract %q sourceFile: %v", ErrInvalidEvidence, record.Kind, record.Name, err)
		}
	}
	if err := validateEvidenceRelativePath(probe.Fixture); err != nil {
		return fmt.Errorf("%w: %s contract %q fixture: %v", ErrInvalidEvidence, record.Kind, record.Name, err)
	}
	switch record.Kind {
	case EventContract:
		if strings.TrimSpace(probe.Producer) == "" {
			return fmt.Errorf("%w: event contract %q probe has no real producer", ErrInvalidEvidence, record.Name)
		}
		if strings.TrimSpace(probe.Delivery) == "" {
			return fmt.Errorf("%w: event contract %q probe has no delivery and replay evidence", ErrInvalidEvidence, record.Name)
		}
	case QueryContract:
		if strings.TrimSpace(probe.Invalidation) == "" {
			return fmt.Errorf("%w: query contract %q probe has no invalidation evidence", ErrInvalidEvidence, record.Name)
		}
	}
	baselineCapture, err := loadAndValidateCapture(evidenceRoot, "baseline", probe.BaselineCapture)
	if err != nil {
		return fmt.Errorf("%w: %s contract %q: %v", ErrInvalidEvidence, record.Kind, record.Name, err)
	}
	candidateCapture, err := loadAndValidateCapture(evidenceRoot, "candidate", probe.CandidateCapture)
	if err != nil {
		return fmt.Errorf("%w: %s contract %q: %v", ErrInvalidEvidence, record.Kind, record.Name, err)
	}
	if err := CompareJSONMode(baselineCapture, candidateCapture, probe.Normalization, probe.ComparisonMode); err != nil {
		return fmt.Errorf("%w: %s contract %q differential comparison: %v", ErrInvalidEvidence, record.Kind, record.Name, err)
	}
	return nil
}

var sha256Pattern = regexp.MustCompile("^[a-f0-9]{64}$")

func loadAndValidateCapture(evidenceRoot string, label string, capture CaptureRef) ([]byte, error) {
	if strings.TrimSpace(capture.Runtime) == "" {
		return nil, fmt.Errorf("%s capture runtime is empty", label)
	}
	if _, err := time.Parse(time.RFC3339, capture.CapturedAt); err != nil {
		return nil, fmt.Errorf("%s capture capturedAt is invalid: %v", label, err)
	}
	if !sha256Pattern.MatchString(capture.SHA256) {
		return nil, fmt.Errorf("%s capture sha256 is invalid", label)
	}
	if err := validateEvidenceRelativePath(capture.Artifact); err != nil {
		return nil, fmt.Errorf("%s capture artifact escapes evidence root: %v", label, err)
	}

	root, err := filepath.Abs(evidenceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence root: %w", err)
	}
	root = filepath.Clean(root)
	artifact := filepath.Join(root, filepath.FromSlash(capture.Artifact))
	relative, err := filepath.Rel(root, artifact)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s capture artifact escapes evidence root", label)
	}
	info, err := os.Lstat(artifact)
	if err != nil {
		return nil, fmt.Errorf("%s capture artifact cannot be read: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s capture artifact must be a regular non-symlink file", label)
	}
	content, err := os.ReadFile(artifact)
	if err != nil {
		return nil, fmt.Errorf("%s capture read: %w", label, err)
	}
	digest := sha256.Sum256(content)
	actualHash := hex.EncodeToString(digest[:])
	if actualHash != capture.SHA256 {
		return nil, fmt.Errorf("%s capture sha256 mismatch: got %s, want %s", label, actualHash, capture.SHA256)
	}
	return content, nil
}

func validateEvidenceRelativePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return errors.New("path must be a non-root relative path")
	}
	if strings.ContainsRune(path, '\\') {
		return errors.New("path must use slash separators")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("path escapes evidence root")
	}
	return nil
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

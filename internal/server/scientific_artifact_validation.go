package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	maxScientificArtifactValidatorOutput = 1 << 20
	scientificArtifactValidationTimeout  = 45 * time.Second
)

var (
	errInvalidScientificArtifact               = errors.New("scientific artifact is invalid")
	errScientificArtifactValidationUnavailable = errors.New("scientific artifact validation is unavailable")
)

type scientificArtifactValidationError struct {
	Code string
}

func (err *scientificArtifactValidationError) Error() string {
	if err == nil || err.Code == "" {
		return errInvalidScientificArtifact.Error()
	}
	return errInvalidScientificArtifact.Error() + ": " + err.Code
}

func (err *scientificArtifactValidationError) Unwrap() error {
	return errInvalidScientificArtifact
}

type scientificArtifactValidation struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Format               string `json:"format"`
	OK                   bool   `json:"ok"`
	Code                 string `json:"code"`
	DelimiterCount       int    `json:"delimiterCount"`
	SupplierRecordCount  int    `json:"supplierRecordCount"`
	ParsedCount          int    `json:"parsedCount"`
	InvalidRecordIndexes []int  `json:"invalidRecordIndexes"`
	AtomCounts           []int  `json:"atomCounts"`
	TerminalDelimiter    bool   `json:"terminalDelimiter"`
	RDKitVersion         string `json:"rdkitVersion,omitempty"`
	Error                string `json:"error,omitempty"`
}

type cappedScientificOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (writer *cappedScientificOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := writer.limit - writer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = writer.buffer.Write(value[:remaining])
			writer.overflow = true
		} else {
			_, _ = writer.buffer.Write(value)
		}
	} else if len(value) > 0 {
		writer.overflow = true
	}
	return original, nil
}

func (writer *cappedScientificOutput) String() string {
	return writer.buffer.String()
}

func (s *Server) validateSDFArtifact(ctx context.Context, reader io.Reader) (scientificArtifactValidation, error) {
	return s.validateMoleculeArtifact(ctx, reader, "sdf")
}

func (s *Server) validateSMILESArtifact(ctx context.Context, reader io.Reader) (scientificArtifactValidation, error) {
	return s.validateMoleculeArtifact(ctx, reader, "smi")
}

func decodeScientificArtifactValidation(
	reader io.Reader,
	format string,
	expectedRDKit string,
) (scientificArtifactValidation, error) {
	if reader == nil {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator returned an invalid result")
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator returned an invalid result")
	}
	var fields map[string]json.RawMessage
	fieldDecoder := json.NewDecoder(bytes.NewReader(raw))
	if err := fieldDecoder.Decode(&fields); err != nil {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator returned an invalid result")
	}
	var fieldTrailing any
	if err := fieldDecoder.Decode(&fieldTrailing); !errors.Is(err, io.EOF) {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator returned trailing data")
	}
	allowedFields := map[string]struct{}{
		"schemaVersion": {}, "format": {}, "ok": {}, "code": {},
		"delimiterCount": {}, "supplierRecordCount": {}, "parsedCount": {},
		"invalidRecordIndexes": {}, "atomCounts": {}, "terminalDelimiter": {},
		"rdkitVersion": {}, "error": {},
	}
	for field := range fields {
		if _, ok := allowedFields[field]; !ok {
			return scientificArtifactValidation{}, errors.New("scientific artifact validator returned an invalid result")
		}
	}
	var result scientificArtifactValidation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator returned an invalid result")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator returned trailing data")
	}
	if result.SchemaVersion != 1 || result.Format != format || result.Code == "" ||
		result.DelimiterCount < 0 || result.SupplierRecordCount < 0 || result.ParsedCount < 0 ||
		len(result.InvalidRecordIndexes) > result.DelimiterCount || len(result.AtomCounts) > result.DelimiterCount {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator result violates its contract")
	}
	if result.RDKitVersion != "" && result.RDKitVersion != expectedRDKit {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator used the wrong RDKit generation")
	}
	return result, nil
}

func (s *Server) validateMoleculeArtifact(
	ctx context.Context,
	reader io.Reader,
	format string,
) (scientificArtifactValidation, error) {
	if s == nil || s.kernelManager == nil || reader == nil {
		return scientificArtifactValidation{}, errScientificArtifactValidationUnavailable
	}
	if format != "sdf" && format != "smi" {
		return scientificArtifactValidation{}, errScientificArtifactValidationUnavailable
	}
	python, validator, generation, expectedRDKit, err := s.kernelManager.ScientificArtifactValidator()
	if err != nil {
		return scientificArtifactValidation{}, errScientificArtifactValidationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	validationContext, cancel := context.WithTimeout(ctx, scientificArtifactValidationTimeout)
	defer cancel()
	arguments := []string{"-I", validator}
	if format != "sdf" {
		arguments = append(arguments, "--format", format)
	}
	command := exec.CommandContext(validationContext, python, arguments...)
	command.Stdin = io.LimitReader(reader, agentSavedArtifactRegularLimit+1)
	prefix := filepath.Dir(filepath.Dir(python))
	command.Env = []string{
		"PATH=" + filepath.Join(prefix, "bin"),
		"CONDA_PREFIX=" + prefix,
		"CONDA_DEFAULT_ENV=synon-biomed-python",
		"PYTHONNOUSERSITE=1",
		"PYTHONUNBUFFERED=1",
		"LANG=C.UTF-8",
		"SYNON_RUNTIME_GENERATION=" + generation,
	}
	stdout := &cappedScientificOutput{limit: maxScientificArtifactValidatorOutput}
	stderr := &cappedScientificOutput{limit: maxScientificArtifactValidatorOutput}
	command.Stdout, command.Stderr = stdout, stderr
	runErr := command.Run()
	if validationContext.Err() != nil {
		return scientificArtifactValidation{}, errors.New("scientific artifact validation timed out")
	}
	if stdout.overflow || stderr.overflow {
		return scientificArtifactValidation{}, errors.New("scientific artifact validator output exceeded its bound")
	}
	result, err := decodeScientificArtifactValidation(strings.NewReader(stdout.String()), format, expectedRDKit)
	if err != nil {
		return scientificArtifactValidation{}, err
	}
	if runErr != nil || !result.OK {
		code := result.Code
		if code == "" {
			code = "validator_failed"
		}
		return result, &scientificArtifactValidationError{Code: code}
	}
	switch format {
	case "sdf":
		if result.Code != "valid_sdf" || result.DelimiterCount <= 0 || !result.TerminalDelimiter ||
			result.SupplierRecordCount != result.DelimiterCount || result.ParsedCount != result.DelimiterCount ||
			len(result.InvalidRecordIndexes) != 0 || len(result.AtomCounts) != result.DelimiterCount || result.RDKitVersion != expectedRDKit {
			return result, &scientificArtifactValidationError{Code: "validator_contract_mismatch"}
		}
	case "smi":
		if result.Code != "valid_smiles" || result.DelimiterCount <= 0 ||
			result.SupplierRecordCount != result.DelimiterCount || result.ParsedCount != result.DelimiterCount ||
			len(result.InvalidRecordIndexes) != 0 || len(result.AtomCounts) != result.DelimiterCount || result.RDKitVersion != expectedRDKit {
			return result, &scientificArtifactValidationError{Code: "validator_contract_mismatch"}
		}
	}
	for _, atomCount := range result.AtomCounts {
		if atomCount <= 0 {
			return result, &scientificArtifactValidationError{Code: "empty_molecule"}
		}
	}
	return result, nil
}

func (s *Server) validateSessionRunnerScientificArtifacts(
	ctx context.Context,
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) ([]string, error) {
	allowedVersions := map[string]struct{}{}
	latestProduced := map[string]string{}
	for _, commit := range commits {
		versionID := strings.TrimSpace(commit.VersionID)
		if versionID == "" {
			continue
		}
		allowedVersions[versionID] = struct{}{}
		if commit.Relation == transcriptstore.ArtifactRelationProduced && strings.TrimSpace(commit.ArtifactID) != "" {
			latestProduced[commit.ArtifactID] = versionID
		}
	}
	selected := map[string]struct{}{}
	producedArtifactByVersion := map[string]string{}
	for _, versionID := range latestProduced {
		selected[versionID] = struct{}{}
	}
	for artifactID, versionID := range latestProduced {
		producedArtifactByVersion[versionID] = artifactID
	}
	explicitReferences := map[string]struct{}{}
	for _, match := range artifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			versionID := strings.TrimSpace(match[1])
			if _, ok := allowedVersions[versionID]; ok {
				selected[versionID] = struct{}{}
				explicitReferences[versionID] = struct{}{}
			}
		}
	}
	versionIDs := make([]string, 0, len(selected))
	for versionID := range selected {
		versionIDs = append(versionIDs, versionID)
	}
	sort.Strings(versionIDs)
	if len(versionIDs) == 0 {
		return nil, nil
	}
	if s == nil || s.workspaceStore == nil || projectID == "" {
		return nil, errors.New("runner scientific artifact authority is unavailable")
	}
	failures := []string{}
	moleculeSetCounts := map[string]map[string]int{}
	for _, versionID := range versionIDs {
		artifact, _, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(versionID)
		if err != nil {
			return nil, err
		}
		if !found {
			if reader != nil {
				_ = reader.Close()
			}
			if _, explicit := explicitReferences[versionID]; explicit {
				return nil, errors.New("runner scientific artifact version is unavailable")
			}
			artifactID := producedArtifactByVersion[versionID]
			_, artifactFound, lookupErr := s.workspaceStore.GetArtifact(artifactID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if artifactFound {
				return nil, errors.New("runner scientific artifact version is unavailable")
			}
			continue
		}
		if artifact.ProjectID != projectID {
			_ = reader.Close()
			return nil, errors.New("runner scientific artifact version is unavailable")
		}
		extension := strings.ToLower(filepath.Ext(strings.TrimSpace(artifact.Name)))
		if extension != ".sdf" && extension != ".smi" && extension != ".smiles" {
			_ = reader.Close()
			continue
		}
		format := "sdf"
		var validation scientificArtifactValidation
		var validationErr error
		if extension == ".sdf" {
			validation, validationErr = s.validateSDFArtifact(ctx, reader)
		} else {
			format = "smi"
			validation, validationErr = s.validateSMILESArtifact(ctx, reader)
		}
		closeErr := reader.Close()
		if validationErr != nil {
			var invalid *scientificArtifactValidationError
			if errors.As(validationErr, &invalid) {
				failures = append(failures, invalid.Code)
				continue
			}
			return nil, validationErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close runner scientific artifact: %w", closeErr)
		}
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(strings.TrimSpace(artifact.Name)), extension))
		if moleculeSetCounts[stem] == nil {
			moleculeSetCounts[stem] = map[string]int{}
		}
		moleculeSetCounts[stem][format] = validation.ParsedCount
	}
	failures = append(failures, moleculeSetRecordCountFailures(moleculeSetCounts)...)
	sort.Strings(failures)
	return failures, nil
}

func moleculeSetRecordCountFailures(moleculeSetCounts map[string]map[string]int) []string {
	failures := []string{}
	for stem, counts := range moleculeSetCounts {
		sdfCount, hasSDF := counts["sdf"]
		smilesCount, hasSMILES := counts["smi"]
		if hasSDF && hasSMILES && sdfCount != smilesCount {
			failures = append(failures, fmt.Sprintf(
				"molecule_set_record_count_mismatch:%s sdf=%d smiles=%d", stem, sdfCount, smilesCount,
			))
		}
	}
	sort.Strings(failures)
	return failures
}

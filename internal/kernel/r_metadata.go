package kernel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxRMetadataBytes     = 16 * 1024 * 1024
	maxRMetadataLineBytes = 64 * 1024
	maxRPackageOperations = 10_000
)

type RPackageOperation struct {
	Timestamp time.Time  `json:"timestamp"`
	Operation string     `json:"operation"`
	Packages  []string   `json:"packages"`
	Result    string     `json:"result"`
	GitHubRef StringList `json:"github_ref,omitempty"`
	sequence  int
}

type StringList []string

func (values StringList) MarshalJSON() ([]byte, error) {
	if len(values) == 1 {
		return json.Marshal(values[0])
	}
	return json.Marshal([]string(values))
}

type RuntimeEnvironmentMetadata struct {
	EnvironmentName string              `json:"environment_name"`
	Language        string              `json:"language"`
	OperationLog    []RPackageOperation `json:"op_log"`
}

func (m *Manager) ReadRuntimeEnvironmentMetadata(environment string) (RuntimeEnvironmentMetadata, error) {
	metadata := RuntimeEnvironmentMetadata{EnvironmentName: strings.TrimSpace(environment), Language: "r", OperationLog: []RPackageOperation{}}
	if m == nil {
		return metadata, errors.New("kernel manager is not configured")
	}
	if !ValidEnvironmentName(metadata.EnvironmentName) {
		return metadata, errors.New("environment name must be a bounded path-free identifier")
	}
	prefix, err := filepath.EvalSymlinks(filepath.Join(m.config.CondaEnvsPath, metadata.EnvironmentName))
	if err != nil || !filepath.IsAbs(prefix) {
		return metadata, errors.New("managed environment is unavailable")
	}
	sequence := 0
	if raw, found, err := readBoundedKernelMetadataFile(prefix, ".operon_metadata.json", maxRMetadataBytes); err != nil {
		return metadata, err
	} else if found {
		var document map[string]json.RawMessage
		if err := json.Unmarshal(raw, &document); err != nil {
			return metadata, errors.New("R environment metadata is invalid")
		}
		if encoded := document["op_log"]; len(encoded) != 0 && !bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
			var entries []json.RawMessage
			if err := json.Unmarshal(encoded, &entries); err != nil {
				return metadata, errors.New("R environment operation log is invalid")
			}
			for _, entry := range entries {
				operation, err := decodeRPackageOperation(entry, sequence)
				if err != nil {
					continue
				}
				metadata.OperationLog = append(metadata.OperationLog, operation)
				sequence++
				if len(metadata.OperationLog) > maxRPackageOperations {
					return metadata, errors.New("R environment operation log exceeds the entry limit")
				}
			}
		}
	}
	if raw, found, err := readBoundedKernelMetadataFile(prefix, ".operon_metadata.r.ndjson", maxRMetadataBytes); err != nil {
		return metadata, err
	} else if found {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), maxRMetadataLineBytes)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			operation, err := decodeRPackageOperation(line, sequence)
			if err != nil {
				continue
			}
			metadata.OperationLog = append(metadata.OperationLog, operation)
			sequence++
			if len(metadata.OperationLog) > maxRPackageOperations {
				return metadata, errors.New("R environment operation log exceeds the entry limit")
			}
		}
		if err := scanner.Err(); err != nil {
			return metadata, errors.New("R environment operation log exceeds the line limit")
		}
	}
	sort.SliceStable(metadata.OperationLog, func(left, right int) bool {
		if metadata.OperationLog[left].Timestamp.Equal(metadata.OperationLog[right].Timestamp) {
			return metadata.OperationLog[left].sequence < metadata.OperationLog[right].sequence
		}
		return metadata.OperationLog[left].Timestamp.Before(metadata.OperationLog[right].Timestamp)
	})
	return metadata, nil
}

func decodeRPackageOperation(raw []byte, sequence int) (RPackageOperation, error) {
	var wire struct {
		Timestamp string          `json:"timestamp"`
		Operation string          `json:"operation"`
		Packages  []string        `json:"packages"`
		Result    string          `json:"result"`
		GitHubRef json.RawMessage `json:"github_ref,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return RPackageOperation{}, errors.New("R package operation is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RPackageOperation{}, errors.New("R package operation is invalid")
	}
	timestamp, err := time.Parse(time.RFC3339, wire.Timestamp)
	if err != nil || timestamp.Location() != time.UTC {
		return RPackageOperation{}, errors.New("R package operation timestamp is invalid")
	}
	switch wire.Operation {
	case "r_cran_install", "r_bioc_install", "r_github_install":
	default:
		return RPackageOperation{}, errors.New("R package operation type is invalid")
	}
	if wire.Result != "success" && wire.Result != "error" {
		return RPackageOperation{}, errors.New("R package operation result is invalid")
	}
	if wire.Packages == nil || len(wire.Packages) > 256 {
		return RPackageOperation{}, errors.New("R package operation packages are invalid")
	}
	for _, name := range wire.Packages {
		if strings.TrimSpace(name) == "" || len(name) > 512 || strings.ContainsAny(name, "\x00\r\n") {
			return RPackageOperation{}, errors.New("R package operation package is invalid")
		}
	}
	githubRefs := StringList{}
	if len(wire.GitHubRef) != 0 && !bytes.Equal(bytes.TrimSpace(wire.GitHubRef), []byte("null")) {
		var single string
		if err := json.Unmarshal(wire.GitHubRef, &single); err == nil {
			githubRefs = append(githubRefs, single)
		} else if err := json.Unmarshal(wire.GitHubRef, (*[]string)(&githubRefs)); err != nil {
			return RPackageOperation{}, errors.New("R package operation GitHub reference is invalid")
		}
	}
	if len(githubRefs) > 256 {
		return RPackageOperation{}, errors.New("R package operation GitHub reference is invalid")
	}
	for _, reference := range githubRefs {
		if strings.TrimSpace(reference) == "" || len(reference) > 512 || strings.ContainsAny(reference, "\x00\r\n") {
			return RPackageOperation{}, errors.New("R package operation GitHub reference is invalid")
		}
	}
	return RPackageOperation{
		Timestamp: timestamp.UTC(), Operation: wire.Operation, Packages: append([]string(nil), wire.Packages...),
		Result: wire.Result, GitHubRef: githubRefs, sequence: sequence,
	}, nil
}

func metadataReadError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("read R environment metadata %s: %w", name, err)
}

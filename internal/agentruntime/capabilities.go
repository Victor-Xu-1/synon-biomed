package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/assets"
)

type agentCapabilityAssignment struct {
	Name       string   `json:"name"`
	Skills     []string `json:"skills"`
	Connectors []string `json:"connectors"`
}

type agentCapabilityManifest struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Agents        []agentCapabilityAssignment `json:"agents"`
}

func loadAgentCapabilities(root string, manifest assets.Manifest) (map[string]agentCapabilityAssignment, error) {
	const relativePath = "capabilities.json"
	if !manifestContainsPath(manifest, relativePath) {
		return nil, errors.New("agent catalog manifest does not include capabilities.json")
	}
	raw, err := os.ReadFile(filepath.Join(root, relativePath))
	if err != nil {
		return nil, fmt.Errorf("read agent capability manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decoded agentCapabilityManifest
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode agent capability manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("agent capability manifest must contain one JSON value")
	}
	if decoded.SchemaVersion != 1 {
		return nil, errors.New("invalid agent capability manifest metadata")
	}
	knownAgents := make(map[string]struct{}, len(manifest.Agents))
	for _, directory := range manifest.Agents {
		knownAgents[normalizeAgentName(directory)] = struct{}{}
	}
	result := make(map[string]agentCapabilityAssignment, len(decoded.Agents))
	for _, item := range decoded.Agents {
		item.Name = normalizeAgentName(item.Name)
		if item.Name == "" || item.Name == "OPERON" {
			return nil, fmt.Errorf("invalid agent capability assignment name %q", item.Name)
		}
		if _, ok := knownAgents[item.Name]; !ok {
			return nil, fmt.Errorf("agent capability assignment references unknown agent %q", item.Name)
		}
		if _, duplicate := result[item.Name]; duplicate {
			return nil, fmt.Errorf("duplicate agent capability assignment %q", item.Name)
		}
		if err := validateCapabilityNames(item.Name, "skill", item.Skills, false); err != nil {
			return nil, err
		}
		if err := validateCapabilityNames(item.Name, "connector", item.Connectors, true); err != nil {
			return nil, err
		}
		result[item.Name] = agentCapabilityAssignment{
			Name: item.Name, Skills: append([]string(nil), item.Skills...),
			Connectors: append([]string(nil), item.Connectors...),
		}
	}
	return result, nil
}

func validateCapabilityNames(agentName, kind string, values []string, requireBundled bool) error {
	if len(values) == 0 {
		return fmt.Errorf("agent %s has no %s assignments", agentName, kind)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || (requireBundled && !strings.HasPrefix(value, "bundled:")) {
			return fmt.Errorf("agent %s has invalid %s assignment %q", agentName, kind, value)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("agent %s has duplicate %s assignment %q", agentName, kind, value)
		}
		seen[key] = struct{}{}
	}
	return nil
}

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const maxResearchIntegrityArtifactBytes = 16 << 20

var researchArtifactSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type researchProducedArtifact struct {
	artifactID string
	name       string
	versionID  string
	sha256     string
	content    []byte
}

const researchProvenanceManifestSchemaV1 = "synon.research_provenance_manifest.v1"

type researchProvenanceManifestV1 struct {
	Schema            string                         `json:"schema"`
	ArtifactsProduced []researchProvenanceArtifactV1 `json:"artifacts_produced"`
}

type researchProvenanceArtifactV1 struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
}

type researchArtifactValidation struct {
	Failures         []string
	ManifestFound    bool
	SelectedVersions map[string]struct{}
}

func (s *Server) validateSessionRunnerResearchArtifacts(
	ctx context.Context,
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
) ([]string, error) {
	result, err := s.validateSessionRunnerResearchArtifactSelection(ctx, projectID, commits)
	return result.Failures, err
}

func (s *Server) validateSessionRunnerResearchArtifactSelection(
	ctx context.Context,
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
) (researchArtifactValidation, error) {
	result := researchArtifactValidation{SelectedVersions: map[string]struct{}{}}
	type orderedCommit struct {
		commit transcriptstore.ArtifactReferenceInput
		index  int
	}
	latestProduced := map[string]orderedCommit{}
	for index, commit := range commits {
		if commit.Relation != transcriptstore.ArtifactRelationProduced ||
			strings.TrimSpace(commit.ArtifactID) == "" || strings.TrimSpace(commit.VersionID) == "" {
			continue
		}
		commit.ArtifactID = strings.TrimSpace(commit.ArtifactID)
		commit.VersionID = strings.TrimSpace(commit.VersionID)
		latestProduced[commit.ArtifactID] = orderedCommit{commit: commit, index: index}
	}
	ordered := make([]orderedCommit, 0, len(latestProduced))
	for _, value := range latestProduced {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].index < ordered[right].index })
	if len(ordered) == 0 {
		return result, nil
	}
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(projectID) == "" {
		return result, errors.New("runner research artifact authority is unavailable")
	}
	producedByVersion := map[string]researchProducedArtifact{}
	latestProducedByName := map[string]researchProducedArtifact{}
	for _, value := range ordered {
		versionID := value.commit.VersionID
		artifact, version, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(versionID)
		if err != nil {
			return result, err
		}
		if !found {
			if reader != nil {
				_ = reader.Close()
			}
			_, artifactFound, lookupErr := s.workspaceStore.GetArtifact(value.commit.ArtifactID)
			if lookupErr != nil {
				return result, lookupErr
			}
			if artifactFound {
				return result, errors.New("runner research artifact version is unavailable")
			}
			continue
		}
		if artifact.ProjectID != projectID || strings.TrimSpace(artifact.ID) != value.commit.ArtifactID {
			_ = reader.Close()
			return result, errors.New("runner research artifact version is unavailable")
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, maxResearchIntegrityArtifactBytes+1))
		closeErr := reader.Close()
		if readErr != nil {
			return result, fmt.Errorf("read runner research artifact: %w", readErr)
		}
		if closeErr != nil {
			return result, fmt.Errorf("close runner research artifact: %w", closeErr)
		}
		if len(content) > maxResearchIntegrityArtifactBytes {
			return result, errors.New("runner research integrity artifact exceeds its size bound")
		}
		name := filepath.ToSlash(strings.TrimSpace(artifact.Name))
		produced := researchProducedArtifact{
			artifactID: artifact.ID, name: name, versionID: version.ID,
			sha256: strings.ToLower(strings.TrimSpace(version.ContentSHA256)), content: content,
		}
		producedByVersion[version.ID] = produced
		latestProducedByName[name] = produced
	}

	if manifest, exists := latestProducedByName["provenance_manifest.json"]; exists {
		result.ManifestFound = true
		manifestFailures, selected := validateResearchProvenanceManifest(manifest.content, producedByVersion)
		result.Failures = append(result.Failures, manifestFailures...)
		for _, artifact := range selected {
			result.SelectedVersions[artifact.versionID] = struct{}{}
		}
		if graph, selectedGraph := selected["evidence_graph.json"]; selectedGraph {
			result.Failures = append(result.Failures, validateResearchEvidenceGraph(graph.content)...)
		}
	} else if graph, exists := latestProducedByName["evidence_graph.json"]; exists {
		result.Failures = append(result.Failures, validateResearchEvidenceGraph(graph.content)...)
	}
	sort.Strings(result.Failures)
	return result, nil
}

func validateResearchEvidenceGraph(content []byte) []string {
	var root map[string]json.RawMessage
	if err := decodeResearchJSON(content, &root); err != nil {
		return []string{"evidence_graph.json:invalid_json"}
	}
	var sections map[string]json.RawMessage
	if json.Unmarshal(root["nodes"], &sections) != nil || len(sections) == 0 {
		return []string{"evidence_graph.json:missing_nodes"}
	}
	nodeIDs := map[string]struct{}{}
	claimIDs := map[string]struct{}{}
	failures := make([]string, 0)
	sectionNames := make([]string, 0, len(sections))
	for section := range sections {
		sectionNames = append(sectionNames, section)
	}
	sort.Strings(sectionNames)
	for _, section := range sectionNames {
		var nodes []map[string]any
		if json.Unmarshal(sections[section], &nodes) != nil {
			failures = append(failures, "evidence_graph.json:nodes_not_array:"+boundedResearchToken(section))
			continue
		}
		for index, node := range nodes {
			id := strings.TrimSpace(stringValue(node["id"]))
			if id == "" {
				failures = append(failures, fmt.Sprintf("evidence_graph.json:node_missing_id:%s:%d", boundedResearchToken(section), index))
				continue
			}
			if _, exists := nodeIDs[id]; exists {
				failures = append(failures, "evidence_graph.json:duplicate_node_id:"+boundedResearchToken(id))
				continue
			}
			nodeIDs[id] = struct{}{}
			if section == "claims" {
				claimIDs[id] = struct{}{}
			}
		}
	}
	var edges []map[string]any
	if json.Unmarshal(root["edges"], &edges) != nil {
		failures = append(failures, "evidence_graph.json:missing_edges")
		return failures
	}
	incomingClaims := map[string]int{}
	for index, edge := range edges {
		source := strings.TrimSpace(stringValue(edge["source"]))
		target := strings.TrimSpace(stringValue(edge["target"]))
		relation := strings.TrimSpace(stringValue(edge["relation"]))
		if source == "" || target == "" || relation == "" {
			failures = append(failures, fmt.Sprintf("evidence_graph.json:invalid_edge:%d", index))
			continue
		}
		if _, exists := nodeIDs[source]; !exists {
			failures = append(failures, "evidence_graph.json:dangling_edge_source:"+boundedResearchToken(source))
		}
		if _, exists := nodeIDs[target]; !exists {
			failures = append(failures, "evidence_graph.json:dangling_edge_target:"+boundedResearchToken(target))
		} else if _, isClaim := claimIDs[target]; isClaim {
			incomingClaims[target]++
		}
	}
	for claimID := range claimIDs {
		if incomingClaims[claimID] == 0 {
			failures = append(failures, "evidence_graph.json:unsupported_claim:"+boundedResearchToken(claimID))
		}
	}
	return failures
}

func validateResearchProvenanceManifest(
	content []byte,
	producedByVersion map[string]researchProducedArtifact,
) ([]string, map[string]researchProducedArtifact) {
	var manifest researchProvenanceManifestV1
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return []string{"provenance_manifest.json:invalid_json"}, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return []string{"provenance_manifest.json:invalid_json"}, nil
	}
	if manifest.Schema != researchProvenanceManifestSchemaV1 {
		return []string{"provenance_manifest.json:invalid_schema"}, nil
	}
	if len(manifest.ArtifactsProduced) == 0 {
		return []string{"provenance_manifest.json:missing_artifacts_produced"}, nil
	}
	failures := make([]string, 0)
	selected := map[string]researchProducedArtifact{}
	seenArtifacts := map[string]struct{}{}
	seenVersions := map[string]struct{}{}
	seenNames := map[string]struct{}{}
	for index, entry := range manifest.ArtifactsProduced {
		artifactID := strings.TrimSpace(entry.ArtifactID)
		versionID := strings.TrimSpace(entry.VersionID)
		name := filepath.ToSlash(strings.TrimSpace(entry.Filename))
		hash := strings.TrimSpace(entry.SHA256)
		if name != "" && (name == "." || strings.HasPrefix(name, "/") || strings.Contains(name, "..")) {
			failures = append(failures, fmt.Sprintf("provenance_manifest.json:invalid_filename:%d", index))
			continue
		}
		if !validResearchArtifactIdentity(artifactID) {
			failures = append(failures, fmt.Sprintf("provenance_manifest.json:invalid_artifact_id:%d", index))
			continue
		}
		if !validResearchArtifactIdentity(versionID) {
			failures = append(failures, fmt.Sprintf("provenance_manifest.json:invalid_version_id:%d", index))
			continue
		}
		if _, duplicate := seenArtifacts[artifactID]; duplicate {
			failures = append(failures, "provenance_manifest.json:duplicate_artifact_id:"+boundedResearchToken(artifactID))
			continue
		}
		if _, duplicate := seenVersions[versionID]; duplicate {
			failures = append(failures, "provenance_manifest.json:duplicate_version_id:"+boundedResearchToken(versionID))
			continue
		}
		seenArtifacts[artifactID] = struct{}{}
		seenVersions[versionID] = struct{}{}
		if name == "provenance_manifest.json" {
			failures = append(failures, "provenance_manifest.json:self_reference_forbidden")
			continue
		}
		if !researchArtifactSHA256Pattern.MatchString(hash) {
			failures = append(failures, "provenance_manifest.json:invalid_sha256:"+boundedResearchToken(versionID))
			continue
		}
		produced, exists := producedByVersion[versionID]
		if !exists {
			failures = append(failures, "provenance_manifest.json:unbound_version:"+boundedResearchToken(versionID))
			continue
		}
		if produced.artifactID != artifactID {
			failures = append(failures, "provenance_manifest.json:artifact_id_mismatch:"+boundedResearchToken(versionID))
			continue
		}
		if name != "" && produced.name != name {
			failures = append(failures, "provenance_manifest.json:filename_mismatch:"+boundedResearchToken(name))
			continue
		}
		if produced.name == "provenance_manifest.json" {
			failures = append(failures, "provenance_manifest.json:self_reference_forbidden")
			continue
		}
		if _, duplicate := seenNames[produced.name]; duplicate {
			failures = append(failures, "provenance_manifest.json:duplicate_filename:"+boundedResearchToken(produced.name))
			continue
		}
		if hash != produced.sha256 {
			failures = append(failures, "provenance_manifest.json:sha256_mismatch:"+boundedResearchToken(produced.name))
			continue
		}
		seenNames[produced.name] = struct{}{}
		selected[produced.name] = produced
	}
	return failures, selected
}

func validResearchArtifactIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "{}\x00\r\n\t ")
}

func decodeResearchJSON(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func boundedResearchToken(value string) string {
	value = strings.Map(func(char rune) rune {
		if char == ':' || char == '/' || char == '\\' || char == ' ' || char == '\t' || char == '\r' || char == '\n' {
			return '_'
		}
		return char
	}, strings.TrimSpace(value))
	if len(value) > 96 {
		value = value[:96]
	}
	return value
}

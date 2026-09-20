package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const maxRunnerCrossArtifactScanBytes = 16 << 20

var (
	runnerCrossArtifactPathPattern           = regexp.MustCompile("(?i)(?:outputs|reports)/([A-Za-z0-9][A-Za-z0-9_.-]*\\.[A-Za-z0-9]+)")
	runnerCrossArtifactMissingPackagePattern = regexp.MustCompile("(?im)^\\s*([A-Za-z][A-Za-z0-9_.-]*)\\s+MISSING\\b")
)

type runnerCrossArtifactSnapshot struct {
	name string
	text string
}

// validateSessionRunnerCrossArtifactConsistency is a domain-neutral final
// gate for references between durable deliverables. It catches two classes of
// defects that a per-file parser cannot see: a report citing an unpublished
// output, and an environment inventory that contradicts a version claim in a
// companion deliverable. It does not infer scientific values or rewrite files.
func (s *Server) validateSessionRunnerCrossArtifactConsistency(
	ctx context.Context,
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
	finalContents ...string,
) ([]string, error) {
	finalContent := ""
	if len(finalContents) > 0 {
		finalContent = strings.TrimSpace(finalContents[0])
	}
	latest := make(map[string]transcriptstore.ArtifactReferenceInput)
	for _, commit := range commits {
		if commit.Relation != transcriptstore.ArtifactRelationProduced ||
			strings.TrimSpace(commit.ArtifactID) == "" || strings.TrimSpace(commit.VersionID) == "" {
			continue
		}
		latest[commit.ArtifactID] = commit
	}
	if len(latest) == 0 {
		return nil, nil
	}
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(projectID) == "" {
		return nil, errors.New("runner cross-artifact authority is unavailable")
	}
	snapshots := make([]runnerCrossArtifactSnapshot, 0, len(latest))
	producedNames := make(map[string]struct{}, len(latest))
	authoritativeArtifactReferences := make(map[string]map[string]struct{})
	for _, commit := range latest {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		artifact, _, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(commit.VersionID)
		if err != nil {
			return nil, err
		}
		if !found {
			if reader != nil {
				_ = reader.Close()
			}
			_, artifactFound, lookupErr := s.workspaceStore.GetArtifact(commit.ArtifactID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if artifactFound {
				return nil, fmt.Errorf("runner cross-artifact version is unavailable: %s", commit.VersionID)
			}
			continue
		}
		if artifact.ProjectID != projectID {
			_ = reader.Close()
			return nil, fmt.Errorf("runner cross-artifact version is unavailable: %s", commit.VersionID)
		}
		name := strings.TrimSpace(artifact.Name)
		// Display names discard directories and are not repair addresses. Reuse
		// the existing project-scoped artifact resolver so a correction targets
		// the saved source, including when another file has the same basename.
		resolution, resolved, resolveErr := s.resolveAgentSavedArtifactReference(commit.VersionID)
		if resolveErr != nil {
			_ = reader.Close()
			return nil, resolveErr
		}
		if resolved && resolution.projectID == projectID {
			if sourcePath, pathErr := normalizeAgentSavedArtifactPath(resolution.relativePath); pathErr == nil {
				name = sourcePath
			}
		}
		producedNames[strings.ToLower(filepath.Base(name))] = struct{}{}
		lineage, lineageFound, lineageErr := s.workspaceStore.GetArtifactVersionLineageRecord(commit.VersionID, false)
		if lineageErr != nil {
			_ = reader.Close()
			return nil, lineageErr
		}
		if lineageFound && !lineage.IsUserUpload && lineage.Language != nil &&
			strings.EqualFold(strings.TrimSpace(*lineage.Language), agentPublicScientificArtifactLanguage) {
			if reference, found := runnerCrossArtifactFilenameReference(name); found {
				namespace := runnerCrossArtifactReferenceNamespace(reference)
				if authoritativeArtifactReferences[namespace] == nil {
					authoritativeArtifactReferences[namespace] = make(map[string]struct{})
				}
				authoritativeArtifactReferences[namespace][reference] = struct{}{}
			}
		}
		if !runnerCrossArtifactScanName(name) {
			if reader != nil {
				_ = reader.Close()
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxRunnerCrossArtifactScanBytes+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read runner cross-artifact %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close runner cross-artifact %q: %w", name, closeErr)
		}
		if len(data) > maxRunnerCrossArtifactScanBytes {
			continue
		}
		text, textErr := runnerCrossArtifactVisibleText(name, data)
		if textErr != nil {
			return nil, textErr
		}
		snapshots = append(snapshots, runnerCrossArtifactSnapshot{name: name, text: text})
	}
	if finalContent != "" {
		snapshots = append(snapshots, runnerCrossArtifactSnapshot{name: "final_response.md", text: finalContent})
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return strings.ToLower(snapshots[i].name) < strings.ToLower(snapshots[j].name)
	})

	failures := make([]string, 0)
	seen := make(map[string]struct{})
	appendFailure := func(value string) {
		if _, found := seen[value]; found {
			return
		}
		seen[value] = struct{}{}
		failures = append(failures, value)
	}
	if finalContent != "" && len(authoritativeArtifactReferences) > 0 {
		finalReferences, negative := map[string]struct{}{}, map[string]struct{}{}
		addSessionRunnerCandidateReferences(finalReferences, negative, finalContent)
		for namespace, authoritative := range authoritativeArtifactReferences {
			actual := make(map[string]struct{})
			matched := false
			for reference := range finalReferences {
				if runnerCrossArtifactReferenceNamespace(reference) != namespace {
					continue
				}
				actual[reference] = struct{}{}
				if _, found := authoritative[reference]; found {
					matched = true
				}
			}
			if len(actual) == 0 || matched {
				continue
			}
			appendFailure("authoritative_artifact_reference_conflict:" + namespace +
				" expected=" + strings.Join(runnerCrossArtifactSortedKeys(authoritative), "|") +
				" actual=" + strings.Join(runnerCrossArtifactSortedKeys(actual), "|"))
		}
	}
	for _, snapshot := range snapshots {
		for _, match := range runnerCrossArtifactPathPattern.FindAllStringSubmatch(snapshot.text, -1) {
			if len(match) != 2 {
				continue
			}
			referencedName := strings.ToLower(filepath.Base(match[1]))
			if _, found := producedNames[referencedName]; !found {
				appendFailure("missing_artifact_reference:" + match[1] + " in " + snapshot.name)
			}
		}
	}
	for _, snapshot := range snapshots {
		for _, failure := range runnerMachineValidationFailures(snapshot.name, snapshot.text) {
			appendFailure(failure)
		}
		for _, failure := range runnerCrossArtifactTemplateFailures(snapshot.name, snapshot.text) {
			appendFailure(failure)
		}
		for _, failure := range runnerEvidenceProvenanceFailures(snapshot) {
			appendFailure(failure)
		}
	}
	for _, failure := range runnerCrossArtifactTableFailures(snapshots) {
		appendFailure(failure)
	}

	for _, inventory := range snapshots {
		lowerName := strings.ToLower(filepath.Base(inventory.name))
		if !strings.Contains(lowerName, "env") || !strings.Contains(lowerName, "inventory") {
			continue
		}
		for _, match := range runnerCrossArtifactMissingPackagePattern.FindAllStringSubmatch(inventory.text, -1) {
			if len(match) != 2 {
				continue
			}
			packageName := match[1]
			versionPattern := regexp.MustCompile(
				"(?i)(?:^|[^A-Za-z0-9_.-])" + regexp.QuoteMeta(packageName) +
					"(?:\\s+|[:=]\\s*)v?[0-9]+\\.[0-9]+(?:\\.[0-9]+)*\\b",
			)
			for _, companion := range snapshots {
				if companion.name == inventory.name {
					continue
				}
				for _, location := range versionPattern.FindAllStringIndex(companion.text, -1) {
					start, end := location[0], location[1]
					contextStart := runnerCrossArtifactMaxInt(0, start-80)
					contextEnd := runnerCrossArtifactMinInt(len(companion.text), end+80)
					contextText := strings.ToLower(companion.text[contextStart:contextEnd])
					if strings.Contains(contextText, "missing") ||
						strings.Contains(contextText, "not installed") ||
						strings.Contains(contextText, "unavailable") ||
						strings.Contains(contextText, "缺失") ||
						strings.Contains(contextText, "未安装") ||
						strings.Contains(contextText, "未找到") {
						continue
					}
					appendFailure("environment_claim_conflict:" + packageName + " in " + companion.name)
					break
				}
			}
		}
	}
	sort.Strings(failures)
	return failures, nil
}

func runnerCrossArtifactFilenameReference(name string) (string, bool) {
	base := filepath.Base(strings.TrimSpace(name))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if sessionRunnerPDBExactPattern.MatchString(stem) {
		return "accession:pdb:" + strings.ToUpper(stem), true
	}
	if sessionRunnerUniProtExactPattern.MatchString(stem) {
		return "accession:uniprot:" + strings.ToUpper(stem), true
	}
	if sessionRunnerNCTExactPattern.MatchString(stem) {
		return "nct:" + strings.ToUpper(stem), true
	}
	for _, definition := range sessionRunnerAccessionPatterns {
		match := definition.pattern.FindString(stem)
		if match == stem {
			return "accession:" + definition.namespace + ":" + strings.ToUpper(stem), true
		}
	}
	return "", false
}

func runnerCrossArtifactReferenceNamespace(reference string) string {
	parts := strings.Split(strings.TrimSpace(reference), ":")
	if len(parts) >= 2 && parts[0] == "accession" {
		return strings.Join(parts[:2], ":")
	}
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func runnerCrossArtifactSortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func runnerCrossArtifactScanName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".md", ".txt", ".py", ".json", ".html", ".htm", ".csv", ".tsv", ".yaml", ".yml", ".xml", ".pptx":
		return true
	default:
		return false
	}
}

func runnerCrossArtifactVisibleText(name string, data []byte) (string, error) {
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(name)), ".pptx") {
		return runnerCrossArtifactPPTXText(data)
	}
	return string(data), nil
}

func runnerCrossArtifactPPTXText(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read runner cross-artifact presentation: %w", err)
	}
	var text strings.Builder
	for _, entry := range reader.File {
		path := filepath.ToSlash(entry.Name)
		if !strings.HasPrefix(path, "ppt/slides/slide") || !strings.HasSuffix(path, ".xml") {
			continue
		}
		file, openErr := entry.Open()
		if openErr != nil {
			return "", fmt.Errorf("open runner cross-artifact slide: %w", openErr)
		}
		decoder := xml.NewDecoder(file)
		for {
			token, tokenErr := decoder.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil {
				_ = file.Close()
				return "", fmt.Errorf("decode runner cross-artifact slide: %w", tokenErr)
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "t" {
				continue
			}
			var value string
			if decodeErr := decoder.DecodeElement(&value, &start); decodeErr != nil {
				_ = file.Close()
				return "", fmt.Errorf("decode runner cross-artifact slide text: %w", decodeErr)
			}
			text.WriteString(value)
			text.WriteByte('\n')
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", fmt.Errorf("close runner cross-artifact slide: %w", closeErr)
		}
	}
	return text.String(), nil
}

func runnerCrossArtifactMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func runnerCrossArtifactMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

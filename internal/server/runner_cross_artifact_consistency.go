package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

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
	if ctx == nil {
		ctx = context.Background()
	}
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
	inputs := make([]runnerArtifactScanInput, 0, len(latest))
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
		if closeErr := reader.Close(); closeErr != nil {
			return nil, fmt.Errorf("close runner cross-artifact %q: %w", name, closeErr)
		}
		inputs = append(inputs, s.runnerArtifactScanInput(name, commit.VersionID, projectID))
	}
	if finalContent != "" {
		inputs = append(inputs, runnerArtifactTextInput("final_response.md", finalContent))
	}
	sort.Slice(inputs, func(i, j int) bool {
		return strings.ToLower(inputs[i].name) < strings.ToLower(inputs[j].name)
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
	scanned, err := inspectRunnerArtifactInputs(ctx, inputs, producedNames)
	if err != nil {
		return nil, err
	}
	for _, failure := range scanned {
		appendFailure(failure)
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

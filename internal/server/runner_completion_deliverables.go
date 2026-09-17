package server

import (
	"errors"

	"os"
	"path/filepath"

	"sort"

	"regexp"

	"strings"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

var sessionRunnerExplicitDeliverableNamePattern = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9._-]{0,160}\.(?:csv|tsv|xlsx|parquet|json|jsonl|md|html?|pdf|docx|pptx?|sdf|pdb|pdbqt|cif|mmcif|py|r|ipynb)\b`)
var sessionRunnerExplicitDeliverableFormatPattern = regexp.MustCompile(`(?i)\b(?:csv|tsv|xlsx|parquet|json|jsonl|markdown|md|html?|pdf|docx|pptx?|sdf|pdb|pdbqt|cif|mmcif|ipynb)\b`)

var sessionRunnerDurableArtifactIntentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:downloadable|export(?:ed)?\s+(?:as|to)\s+(?:a\s+)?file|save(?:d)?\s+(?:as|to)\s+(?:a\s+)?file|deliverable\s+files?|file\s+attachments?)\b`),
	regexp.MustCompile(`(?:可(?:以)?下载|导出(?:为|成)?(?:文件|表格|报告|数据)|保存(?:为|成)(?:文件|表格|报告)|交付(?:物|文件)|文件附件)`),
}

func sessionRunnerArtifactNameSatisfiesRequiredDeliverable(taskIntent, name string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if base == "" {
		return false
	}
	for _, explicit := range sessionRunnerExplicitDeliverableNames(taskIntent) {
		if base == strings.ToLower(explicit) {
			return true
		}
	}
	for _, format := range sessionRunnerExplicitDeliverableFormats(taskIntent) {
		if sessionRunnerArtifactMatchesDeliverableFormat(base, format) {
			return true
		}
	}
	return sessionRunnerRequiresDurableArtifact(taskIntent)
}

func missingSessionRunnerRequiredDeliverables(taskIntent string, artifactNames []string) []string {
	missing := make([]string, 0)
	present := make(map[string]struct{}, len(artifactNames))
	for _, name := range artifactNames {
		present[strings.ToLower(filepath.Base(strings.TrimSpace(name)))] = struct{}{}
	}
	for _, name := range sessionRunnerExplicitDeliverableNames(taskIntent) {
		if _, exists := present[strings.ToLower(name)]; !exists {
			missing = appendMissingRunnerDeliverable(missing, "artifact "+name)
		}
	}
	for _, format := range sessionRunnerExplicitDeliverableFormats(taskIntent) {
		if !sessionRunnerPresentHasDeliverableFormat(present, format) {
			missing = appendMissingRunnerDeliverable(missing, "a verified ."+format+" artifact")
		}
	}
	if sessionRunnerRequiresDurableArtifact(taskIntent) && len(present) == 0 {
		missing = appendMissingRunnerDeliverable(missing, "at least one verified downloadable artifact")
	}
	return missing
}

func sessionRunnerPresentHasDeliverableFormat(present map[string]struct{}, format string) bool {
	for name := range present {
		if sessionRunnerArtifactMatchesDeliverableFormat(name, format) {
			return true
		}
	}
	return false
}

func sessionRunnerArtifactMatchesDeliverableFormat(name, format string) bool {
	extension := sessionRunnerCanonicalDeliverableFormat(filepath.Ext(strings.TrimSpace(name)))
	format = sessionRunnerCanonicalDeliverableFormat(format)
	if extension == format {
		return true
	}
	return format == "html" && extension == "htm" || format == "htm" && extension == "html"
}

func sessionRunnerCanonicalDeliverableFormat(value string) string {
	format := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	if format == "markdown" {
		return "md"
	}
	return format
}

// sessionRunnerRequiresDurableArtifact recognizes only explicit, domain-neutral
// requests for a persisted/downloadable file. It deliberately does not infer a
// file type from generic words such as report, data, result, or reproducible,
// and it does not confuse downloading an input dataset with delivering output.
func sessionRunnerRequiresDurableArtifact(taskIntent string) bool {
	for _, pattern := range sessionRunnerDurableArtifactIntentPatterns {
		if pattern.MatchString(taskIntent) {
			return true
		}
	}
	return false
}

// sessionRunnerExplicitDeliverableNames extracts only path-free filenames from
// a clause that explicitly asks to output, save, publish, or deliver them. It
// is intentionally domain-neutral and does not treat mentioned inputs or URLs
// as promised deliverables.
func sessionRunnerExplicitDeliverableNames(taskIntent string) []string {
	text := strings.TrimSpace(taskIntent)
	if text == "" {
		return nil
	}
	result := make([]string, 0)
	seen := map[string]struct{}{}
	for _, match := range sessionRunnerExplicitDeliverableNamePattern.FindAllStringIndex(text, -1) {
		start := match[0]
		clauseStart := sessionRunnerDeliverableClauseStart(text[:start])
		clausePrefix := strings.ToLower(text[clauseStart:start])
		if !containsAny(clausePrefix, []string{
			"output", "outputs", "deliver", "publish", "save", "write",
			"输出", "交付", "发布", "保存", "生成", "产出",
		}) {
			continue
		}
		name := filepath.Base(strings.TrimSpace(text[match[0]:match[1]]))
		key := strings.ToLower(name)
		if name == "" || strings.ContainsAny(name, `/\\`) {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

// sessionRunnerExplicitDeliverableFormats recognizes file formats named in an
// explicit output clause even when the user leaves filenames to the agent.
// This remains domain-neutral: a mentioned input format does not become an
// output requirement, while "deliver an SDF and summary CSV" requires both
// distinct persisted formats before a final answer can close the task.
func sessionRunnerExplicitDeliverableFormats(taskIntent string) []string {
	text := strings.TrimSpace(taskIntent)
	if text == "" {
		return nil
	}
	result := make([]string, 0)
	seen := map[string]struct{}{}
	for _, match := range sessionRunnerExplicitDeliverableFormatPattern.FindAllStringIndex(text, -1) {
		clauseStart := sessionRunnerDeliverableClauseStart(text[:match[0]])
		clausePrefix := strings.ToLower(text[clauseStart:match[0]])
		if !containsAny(clausePrefix, []string{
			"output", "outputs", "deliver", "publish", "save", "write", "export",
			"输出", "交付", "发布", "保存", "生成", "产出", "导出",
		}) {
			continue
		}
		format := sessionRunnerCanonicalDeliverableFormat(text[match[0]:match[1]])
		if _, duplicate := seen[format]; duplicate {
			continue
		}
		seen[format] = struct{}{}
		result = append(result, format)
	}
	sort.Strings(result)
	return result
}

func sessionRunnerDeliverableClauseStart(prefix string) int {
	start := 0
	if boundary := strings.LastIndexAny(prefix, "\n。！？；;:"); boundary >= 0 {
		start = boundary + 1
	}
	// ASCII full stops inside extensions are not sentence boundaries. Only a
	// dot followed by whitespace terminates the output clause.
	if boundary := strings.LastIndex(prefix, ". "); boundary >= 0 && boundary+2 > start {
		start = boundary + 2
	}
	return start
}

func (s *Server) sessionRunnerMissingRequiredDeliverables(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
) ([]string, error) {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil {
		return nil, nil
	}
	if len(sessionRunnerExplicitDeliverableNames(run.TaskIntent)) == 0 &&
		len(sessionRunnerExplicitDeliverableFormats(run.TaskIntent)) == 0 &&
		!sessionRunnerRequiresDurableArtifact(run.TaskIntent) {
		return nil, nil
	}
	projectID := sessionRunnerProjectID(session)
	if projectID == "" {
		return nil, nil
	}
	names := make([]string, 0, len(commits))
	seen := make(map[string]struct{}, len(commits))
	for _, commit := range commits {
		if commit.Relation != transcriptstore.ArtifactRelationProduced || strings.TrimSpace(commit.VersionID) == "" {
			continue
		}
		artifact, _, found, err := s.workspaceStore.GetArtifactVersionMetadata(commit.VersionID)
		if err != nil {
			return nil, err
		}
		if !found || artifact.ProjectID != projectID {
			continue
		}
		retention, retentionFound, retentionErr := s.workspaceStore.ArtifactRetentionMode(artifact.ID)
		if retentionErr != nil {
			return nil, retentionErr
		}
		// An explicitly requested output must be visible as a snapshot. Internal
		// working data is durable for audit and resume, but cannot satisfy a
		// user-facing deliverable that must appear with the final answer.
		if !retentionFound || retention != "snapshot" {
			continue
		}
		name := strings.TrimSpace(artifact.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return missingSessionRunnerRequiredDeliverables(run.TaskIntent, names), nil
}

func appendMissingRunnerDeliverable(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// sessionRunnerMissingLocalArtifactDependencies prevents a final answer or a
// produced executable artifact from presenting a workspace-only file as part
// of a reproducible delivery. The check is deliberately evidence based: it
// considers explicit inline-code paths plus quoted local paths in immutable
// code/config artifacts, and only when those paths resolve to regular files in
// the canonical workspace. Public URLs, prose that merely resembles a
// filename, declared output paths, and paths outside the workspace are not
// inferred as dependencies.
func (s *Server) sessionRunnerMissingLocalArtifactDependencies(
	projectID string,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	finalContent string,
) ([]string, error) {
	if s == nil || strings.TrimSpace(s.fileRoot) == "" || run == nil || run.Transcript == nil ||
		strings.TrimSpace(run.Transcript.Stream.UID) == "" {
		return nil, nil
	}
	produced := make(map[string]struct{}, len(commits))
	for _, commit := range commits {
		if commit.Relation != transcriptstore.ArtifactRelationProduced {
			continue
		}
		if artifactID := strings.TrimSpace(commit.ArtifactID); artifactID != "" {
			produced[artifactID] = struct{}{}
		}
	}
	missing := map[string]struct{}{}
	for _, match := range sessionRunnerInlineCodePattern.FindAllStringSubmatch(finalContent, -1) {
		if len(match) != 2 {
			continue
		}
		if err := s.addMissingSessionRunnerLocalArtifactDependency(
			run.Transcript.Stream.UID, produced, missing, match[1],
		); err != nil {
			return nil, err
		}
	}
	if s.workspaceStore != nil && strings.TrimSpace(projectID) != "" {
		seenVersions := map[string]struct{}{}
		for _, commit := range commits {
			versionID := strings.TrimSpace(commit.VersionID)
			if commit.Relation != transcriptstore.ArtifactRelationProduced || versionID == "" {
				continue
			}
			if _, seen := seenVersions[versionID]; seen {
				continue
			}
			seenVersions[versionID] = struct{}{}
			artifactText, artifactName, textArtifact, err := s.sessionRunnerArtifactVersionText(projectID, versionID)
			if err != nil {
				return nil, err
			}
			if !textArtifact || !sessionRunnerDependencyBearingArtifact(artifactName) {
				continue
			}
			for _, candidate := range sessionRunnerSourceLocalDependencyCandidates(artifactText) {
				if err := s.addMissingSessionRunnerLocalArtifactDependency(
					run.Transcript.Stream.UID, produced, missing, candidate,
				); err != nil {
					return nil, err
				}
			}
		}
	}
	result := make([]string, 0, len(missing))
	for relativePath := range missing {
		result = append(result, relativePath)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Server) addMissingSessionRunnerLocalArtifactDependency(
	streamUID string,
	produced map[string]struct{},
	missing map[string]struct{},
	candidate string,
) error {
	candidate = strings.Trim(strings.TrimSpace(candidate), "\"'")
	candidate = strings.ReplaceAll(candidate, `\`, "/")
	if candidate == "" || strings.Contains(candidate, "://") || strings.ContainsAny(candidate, "\x00\r\n\t<>|*?\"") ||
		strings.Contains(candidate, ":") || filepath.Ext(candidate) == "" {
		return nil
	}
	target, relativePath, err := resolveArtifactPath(s.fileRoot, candidate)
	if err != nil {
		return nil
	}
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	artifactID := streamArtifactIDFor(streamUID, relativePath)
	if _, found := produced[artifactID]; !found {
		missing[relativePath] = struct{}{}
	}
	return nil
}

func sessionRunnerDependencyBearingArtifact(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".py", ".r", ".jl", ".m", ".sh", ".bash", ".zsh", ".ps1", ".bat", ".cmd",
		".js", ".mjs", ".cjs", ".ts", ".tsx", ".go", ".rs", ".java", ".kt", ".scala",
		".c", ".h", ".cc", ".cpp", ".cxx", ".f", ".f90", ".f95", ".ipynb",
		".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf":
		return true
	default:
		return false
	}
}

func sessionRunnerSourceLocalDependencyCandidates(content string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		for _, match := range sessionRunnerQuotedStringPattern.FindAllStringSubmatchIndex(line, -1) {
			if len(match) != 4 || match[2] < 0 || match[3] < match[2] {
				continue
			}
			prefix := line[:match[0]]
			suffix := line[match[1]:]
			if sessionRunnerWriteModePattern.MatchString(suffix) || sessionRunnerOutputCallPattern.MatchString(prefix) {
				continue
			}
			candidate := strings.TrimSpace(line[match[2]:match[3]])
			if candidate == "" || filepath.Ext(strings.ReplaceAll(candidate, `\`, "/")) == "" {
				continue
			}
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			result = append(result, candidate)
		}
	}
	return result
}

package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"synon-go/internal/networkpolicy"
)

const maxManagedEnvironmentSourceReferenceBytes = 2 << 20

var managedEnvironmentHTTPSReferencePattern = regexp.MustCompile(`https://[^\s<>"'\)\]]+`)

// managedEnvironmentSourceEvidence keeps guessed repositories out of a costly
// install without hard-coding any engine. A source is admitted when it appears
// in the currently loaded Skill material or in a successful immutable tool
// result from this logical task. Missing evidence is a recoverable discovery
// step, not a task failure or a reason to change implementations.
func (s *Server) managedEnvironmentSourceEvidence(
	ctx context.Context,
	packages []string,
	structuredPublicSources ...string,
) (map[string]string, []string, error) {
	sources := managedEnvironmentPackageSourceURLs(packages)
	if len(sources) == 0 {
		return nil, nil, nil
	}
	authorities := map[string]string{}
	for _, source := range managedEnvironmentPackageSourceURLs(structuredPublicSources) {
		if managedEnvironmentPublicPackageSource(source) {
			authorities[source] = "runtime:public-package-source"
		}
	}
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if run != nil {
		for _, source := range managedEnvironmentSourceURLsInText(run.TaskIntent) {
			authorities[source] = askUserCurrentTaskEvidenceReference
		}
	}
	if run != nil && s != nil && s.skillCatalog != nil {
		for _, skillName := range run.executedSkillNamesSnapshot() {
			skill, found := findCatalogSkill(s.skillCatalog, skillName)
			if !found {
				continue
			}
			for _, source := range managedEnvironmentSourceURLsInText(skill.Body) {
				if _, known := authorities[source]; !known {
					authorities[source] = "skill:" + skill.Name
				}
			}
			for _, reference := range skill.References {
				path := filepath.Join(filepath.Dir(skill.Path), filepath.FromSlash(reference))
				info, err := os.Stat(path)
				if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxManagedEnvironmentSourceReferenceBytes {
					continue
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				for _, source := range managedEnvironmentSourceURLsInText(string(raw)) {
					if _, known := authorities[source]; !known {
						authorities[source] = "skill:" + skill.Name
					}
				}
			}
		}
	}
	if run != nil && run.Transcript != nil {
		messages, err := s.sessionRunnerDurableExplicitToolContractMessages(ctx, run)
		if err != nil {
			return nil, nil, err
		}
		for _, message := range messages {
			if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
				continue
			}
			var result any
			if json.Unmarshal([]byte(message.Content), &result) != nil ||
				!managedEnvironmentToolResultProvidesSourceEvidence(result) {
				continue
			}
			for _, source := range managedEnvironmentSourceURLsInText(message.Content) {
				authorities[source] = "tool-call:" + strings.TrimSpace(message.ToolCallID)
			}
		}
	}
	evidence := make(map[string]string, len(sources))
	missing := make([]string, 0)
	for _, source := range sources {
		if authority := strings.TrimSpace(authorities[source]); authority != "" {
			evidence[source] = authority
			continue
		}
		missing = append(missing, source)
	}
	return evidence, missing, nil
}

func managedEnvironmentToolResultProvidesSourceEvidence(result any) bool {
	// A rejected/non-executing preflight often repeats the caller's guessed URL
	// in missing_sources. Treating that diagnostic as a successful source read
	// lets the rejected input authorize itself on the next attempt.
	return toolResultProvidesExecutedEvidence(result)
}

// managedEnvironmentPublicPackageSource admits only the credential-free HTTPS
// locations passed through the dedicated pip_find_links or
// pip_extra_index_urls fields. VCS requirements and arbitrary package strings
// still require task or Skill evidence. This removes a model round trip for a
// normal public wheel index without broadening access to loopback, private,
// reserved, credential-bearing, or built-in denied destinations.
func managedEnvironmentPublicPackageSource(source string) bool {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") ||
		parsed.User != nil || strings.TrimSpace(parsed.Path) == "" {
		return false
	}
	host, err := networkpolicy.NormalizePattern(parsed.Hostname())
	return err == nil && !networkpolicy.PrivateOrReserved(host) &&
		networkpolicy.ConflictingPattern(host, networkpolicy.BuiltInDeniedPatterns()) == ""
}

func managedEnvironmentPackageSourceURLs(packages []string) []string {
	values := make([]string, 0)
	for _, requirement := range packages {
		if source := canonicalManagedEnvironmentSourceURL(requirement); source != "" {
			values = append(values, source)
		}
	}
	return uniqueSortedFolded(values)
}

func managedEnvironmentSourceURLsInText(text string) []string {
	values := make([]string, 0)
	for _, raw := range managedEnvironmentHTTPSReferencePattern.FindAllString(text, -1) {
		if source := canonicalManagedEnvironmentSourceURL(raw); source != "" {
			values = append(values, source)
		}
	}
	return uniqueSortedFolded(values)
}

func canonicalManagedEnvironmentSourceURL(requirement string) string {
	value := strings.TrimSpace(requirement)
	if strings.HasPrefix(strings.ToLower(value), "pip::") {
		value = strings.TrimSpace(value[len("pip::"):])
	}
	if separator := strings.Index(value, " @ "); separator > 0 {
		value = strings.TrimSpace(value[separator+3:])
	}
	if strings.HasPrefix(strings.ToLower(value), "git+https://") {
		value = value[len("git+"):]
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") ||
		parsed.User != nil || strings.TrimSpace(parsed.Hostname()) == "" || strings.TrimSpace(parsed.Path) == "" {
		return ""
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if marker := strings.Index(strings.ToLower(path), ".git@"); marker >= 0 {
		path = path[:marker]
	} else {
		path = strings.TrimSuffix(path, ".git")
	}
	if path == "" {
		return ""
	}
	return "https://" + strings.ToLower(parsed.Hostname()) + path
}

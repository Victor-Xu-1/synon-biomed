package server

import (
	"fmt"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type sessionRunnerRequiredDeliverableLink struct {
	Name      string
	VersionID string
}

// sessionRunnerAttachRequiredDeliverables makes the persisted final answer and
// the project artifact tray agree. It only exposes immutable snapshot versions
// that satisfy an explicit deliverable in the user's request; working data and
// unrelated files remain hidden.
func (s *Server) sessionRunnerAttachRequiredDeliverables(
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) (string, error) {
	if s == nil || s.workspaceStore == nil || run == nil {
		return content, nil
	}
	links := make([]sessionRunnerRequiredDeliverableLink, 0)
	seenArtifacts := make(map[string]int)
	for _, commit := range latestSessionRunnerArtifactReferences(commits) {
		if commit.Relation != transcriptstore.ArtifactRelationProduced ||
			!sessionRunnerCanonicalArtifactReferenceIDPattern.MatchString(strings.TrimSpace(commit.VersionID)) {
			continue
		}
		artifact, _, found, err := s.workspaceStore.GetArtifactVersionMetadata(commit.VersionID)
		if err != nil {
			return content, err
		}
		if !found || !sessionRunnerArtifactNameSatisfiesRequiredDeliverable(run.TaskIntent, artifact.Name) {
			continue
		}
		retention, retentionFound, err := s.workspaceStore.ArtifactRetentionMode(artifact.ID)
		if err != nil {
			return content, err
		}
		if !retentionFound || retention != "snapshot" {
			continue
		}
		link := sessionRunnerRequiredDeliverableLink{Name: strings.TrimSpace(artifact.Name), VersionID: strings.TrimSpace(commit.VersionID)}
		if index, exists := seenArtifacts[artifact.ID]; exists {
			links[index] = link
			continue
		}
		seenArtifacts[artifact.ID] = len(links)
		links = append(links, link)
	}
	return appendSessionRunnerRequiredDeliverableLinks(content, run.ResponseLanguage, links), nil
}

func appendSessionRunnerRequiredDeliverableLinks(
	content, language string,
	links []sessionRunnerRequiredDeliverableLink,
) string {
	present := make(map[string]struct{})
	for _, match := range artifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			present[strings.TrimSpace(match[1])] = struct{}{}
		}
	}
	missing := make([]sessionRunnerRequiredDeliverableLink, 0, len(links))
	for _, link := range links {
		if strings.TrimSpace(link.Name) == "" || !sessionRunnerCanonicalArtifactReferenceIDPattern.MatchString(strings.TrimSpace(link.VersionID)) {
			continue
		}
		if _, exists := present[strings.TrimSpace(link.VersionID)]; exists {
			continue
		}
		missing = append(missing, link)
	}
	if len(missing) == 0 {
		return content
	}
	sort.SliceStable(missing, func(i, j int) bool {
		return strings.ToLower(missing[i].Name) < strings.ToLower(missing[j].Name)
	})
	heading := "Deliverables"
	if sessionRunnerRequiresChinese(language) {
		heading = "交付文件"
	}
	var builder strings.Builder
	builder.WriteString(strings.TrimRight(content, " \t\r\n"))
	builder.WriteString("\n\n")
	builder.WriteString(heading)
	builder.WriteString("\n\n")
	for _, link := range missing {
		name := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(link.Name)
		builder.WriteString(fmt.Sprintf("- [%s]({{artifact:%s}})\n", name, link.VersionID))
	}
	return strings.TrimRight(builder.String(), "\n")
}

package server

import (
	"fmt"
	"regexp"

	"strings"

	"synon-go/internal/agentruntime"
)

var sessionRunnerPlaceholderMarkdownDestinationPattern = regexp.MustCompile(`!?\[([^\]\r\n]+)\]\(\s*(?:#\s*)?\)`)
var sessionRunnerNonCanonicalArtifactURIMarkdownPattern = regexp.MustCompile(`(?i)!?\[([^\]\r\n]+)\]\(\s*artifact:[^)\r\n]*\)`)

// Final chat artifact references are immutable artifact-version identities.
// The persistence layer issues UUIDs for those versions; accepting arbitrary
// text here lets model placeholders survive presentation parsing and fail only
// after the user has already seen a completion-looking candidate.
var sessionRunnerCanonicalArtifactReferenceIDPattern = regexp.MustCompile(
	`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

func replaceSessionRunnerFinalMessageContent(result *agentruntime.RunResult, content string) {
	if result == nil || result.FinalMessage.Content == content {
		return
	}
	previous := result.FinalMessage.Content
	result.FinalMessage.Content = content
	for index := len(result.Messages) - 1; index >= 0; index-- {
		if result.Messages[index].Role == "assistant" && result.Messages[index].Content == previous {
			result.Messages[index].Content = content
			return
		}
	}
	// Streaming adapters may retain a whitespace-normalized or incrementally
	// assembled copy in Messages while FinalMessage holds the canonical final
	// candidate. Persistence projects Messages, so update the last assistant
	// entry as the same final answer when exact identity is unavailable.
	for index := len(result.Messages) - 1; index >= 0; index-- {
		if result.Messages[index].Role == "assistant" {
			result.Messages[index].Content = content
			return
		}
	}
}

// normalizeSessionRunnerMalformedArtifactReferences converts only malformed
// artifact presentation to plain visible text. Valid immutable references are
// preserved. The function fails closed when every malformed marker cannot be
// removed without leaving another malformed marker behind.
func normalizeSessionRunnerMalformedArtifactReferences(content string) (string, bool) {
	if len(sessionRunnerArtifactReferenceSyntaxFailures(content)) == 0 {
		return content, false
	}
	normalizedArtifactURIs := sessionRunnerNonCanonicalArtifactURIMarkdownPattern.ReplaceAllString(content, "$1")
	nonCanonicalArtifactURIsChanged := normalizedArtifactURIs != content
	content = normalizedArtifactURIs
	normalizedPlaceholderLinks := sessionRunnerPlaceholderMarkdownDestinationPattern.ReplaceAllString(content, "$1")
	placeholderLinksChanged := normalizedPlaceholderLinks != content
	content = normalizedPlaceholderLinks
	matches := artifactReferencePattern.FindAllStringSubmatchIndex(content, -1)
	matchesByStart := make(map[int][]int, len(matches))
	for _, match := range matches {
		if len(match) >= 4 {
			matchesByStart[match[0]] = match
		}
	}
	var builder strings.Builder
	builder.Grow(len(content))
	cursor := 0
	searchFrom := 0
	changed := nonCanonicalArtifactURIsChanged || placeholderLinksChanged
	for searchFrom < len(content) {
		relative := strings.Index(content[searchFrom:], sessionRunnerArtifactReferenceMarker)
		if relative < 0 {
			break
		}
		start := searchFrom + relative
		match := matchesByStart[start]
		if len(match) >= 4 &&
			sessionRunnerArtifactReferenceHasCanonicalIdentifier(content, match) &&
			sessionRunnerArtifactReferenceHasValidEnvelope(content, match[0], match[1]) {
			searchFrom = match[1]
			continue
		}
		matchEnd := 0
		if len(match) >= 2 {
			matchEnd = match[1]
		}
		preserveCanonicalReference := len(match) >= 4 &&
			sessionRunnerArtifactReferenceHasCanonicalIdentifier(content, match)
		from, to, replacement, ok := sessionRunnerMalformedArtifactReferenceRepairSpan(
			content,
			start,
			matchEnd,
			preserveCanonicalReference,
		)
		if !ok || from < cursor || to <= from || to > len(content) {
			return content, false
		}
		builder.WriteString(content[cursor:from])
		builder.WriteString(replacement)
		cursor = to
		searchFrom = to
		changed = true
	}
	if !changed {
		return content, false
	}
	builder.WriteString(content[cursor:])
	normalized := builder.String()
	if len(sessionRunnerArtifactReferenceSyntaxFailures(normalized)) != 0 {
		return content, false
	}
	return normalized, true
}

func sessionRunnerMalformedArtifactReferenceRepairSpan(
	content string,
	start, matchEnd int,
	preserveCanonicalReference bool,
) (int, int, string, bool) {
	end := sessionRunnerMalformedArtifactReferenceTokenEnd(content, start, matchEnd)
	if end <= start {
		return 0, 0, "", false
	}
	if start < 2 || content[start-2:start] != "](" {
		return start, end, "", true
	}
	labelClose := start - 2
	labelOpen := strings.LastIndexByte(content[:labelClose], '[')
	if labelOpen < 0 {
		return start, end, "", true
	}
	label := content[labelOpen+1 : labelClose]
	if strings.TrimSpace(label) == "" || strings.ContainsAny(label, "[]\r\n") {
		return start, end, "", true
	}
	if labelOpen > 0 && content[labelOpen-1] == '!' {
		replacement := label
		if preserveCanonicalReference && matchEnd > start {
			replacement = content[start:matchEnd]
		}
		return labelOpen - 1, end, replacement, true
	}
	return labelOpen, end, label, true
}

func sessionRunnerMalformedArtifactReferenceTokenEnd(content string, start, matchEnd int) int {
	if matchEnd > start && matchEnd <= len(content) {
		end := matchEnd
		for end < len(content) && content[end] == ')' {
			end++
		}
		return end
	}
	end := start + len(sessionRunnerArtifactReferenceMarker)
	for end < len(content) {
		switch content[end] {
		case ' ', '\t', '\r', '\n':
			return end
		case ')':
			end++
			for end < len(content) && content[end] == ')' {
				end++
			}
			return end
		default:
			end++
		}
	}
	return end
}

func sessionRunnerArtifactReferenceSyntaxFailures(content string) []string {
	matches := artifactReferencePattern.FindAllStringSubmatchIndex(content, -1)
	matchesByStart := make(map[int][]int, len(matches))
	for _, match := range matches {
		if len(match) >= 4 {
			matchesByStart[match[0]] = match
		}
	}
	issues := []string{}
	for _, match := range sessionRunnerNonCanonicalArtifactURIMarkdownPattern.FindAllStringIndex(content, -1) {
		issues = append(issues, fmt.Sprintf("noncanonical_artifact_uri@byte_%d", match[0]))
	}
	for _, match := range sessionRunnerPlaceholderMarkdownDestinationPattern.FindAllStringIndex(content, -1) {
		issues = append(issues, fmt.Sprintf("placeholder_markdown_destination@byte_%d", match[0]))
	}
	for cursor := 0; cursor < len(content); {
		relative := strings.Index(content[cursor:], sessionRunnerArtifactReferenceMarker)
		if relative < 0 {
			break
		}
		start := cursor + relative
		match, found := matchesByStart[start]
		if !found {
			issues = append(issues, fmt.Sprintf("malformed_placeholder@byte_%d", start))
			cursor = start + len(sessionRunnerArtifactReferenceMarker)
			continue
		}
		if !sessionRunnerArtifactReferenceHasCanonicalIdentifier(content, match) {
			issues = append(issues, fmt.Sprintf("invalid_identifier@byte_%d", start))
		}
		if !sessionRunnerArtifactReferenceHasValidEnvelope(content, match[0], match[1]) {
			issues = append(issues, fmt.Sprintf("invalid_envelope@byte_%d", start))
		}
		cursor = match[1]
	}
	return issues
}

func sessionRunnerArtifactReferenceHasCanonicalIdentifier(content string, match []int) bool {
	if len(match) < 4 || match[2] < 0 || match[3] <= match[2] || match[3] > len(content) {
		return false
	}
	return sessionRunnerCanonicalArtifactReferenceIDPattern.MatchString(strings.TrimSpace(content[match[2]:match[3]]))
}

func sessionRunnerArtifactReferenceHasValidEnvelope(content string, start, end int) bool {
	if start < 0 || end <= start || end > len(content) {
		return false
	}
	if sessionRunnerArtifactReferenceWhitespaceBoundary(content, start-1) &&
		sessionRunnerArtifactReferenceWhitespaceBoundary(content, end) {
		return true
	}
	if start < 2 || content[start-2:start] != "](" || end >= len(content) || content[end] != ')' {
		return false
	}
	labelClose := start - 2
	labelOpen := strings.LastIndexByte(content[:labelClose], '[')
	if labelOpen < 0 {
		return false
	}
	label := content[labelOpen+1 : labelClose]
	if strings.TrimSpace(label) == "" || strings.ContainsAny(label, "[]\r\n") {
		return false
	}
	if end+1 < len(content) {
		switch content[end+1] {
		case ')', ']', '}':
			return false
		}
	}
	return true
}

func sessionRunnerArtifactReferenceWhitespaceBoundary(content string, index int) bool {
	if index < 0 || index >= len(content) {
		return true
	}
	switch content[index] {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

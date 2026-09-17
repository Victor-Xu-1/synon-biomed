package server

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var runnerCrossArtifactMustachePattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
var runnerCrossArtifactFormatFieldPattern = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*(?:![rsa])?(?::[^{}\r\n]{1,40})?\}`)

func runnerCrossArtifactTemplateFailures(name, content string) []string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".md", ".txt", ".html", ".htm", ".csv", ".tsv":
	default:
		return nil
	}
	if runnerCrossArtifactWholeFileScriptTemplate(content) {
		return []string{"unresolved_template_marker:" + name}
	}
	if strings.Contains(content, "{%") || strings.Contains(content, "%}") {
		return []string{"unresolved_template_marker:" + name}
	}
	// User-facing text is published as final materialized bytes, not a format
	// template. Restrict this check to identifier-shaped Python/str.format
	// fields so mathematical braces, chemical notation, JSON examples, and
	// canonical artifact references remain valid.
	withoutArtifactReferences := runnerCrossArtifactMustachePattern.ReplaceAllStringFunc(content, func(value string) string {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "{{"), "}}"))
		if strings.HasPrefix(strings.ToLower(inner), "artifact:") {
			return ""
		}
		return value
	})
	if runnerCrossArtifactHasUnrenderedFormatField(stripRunnerMarkdownMathForTemplateCheck(withoutArtifactReferences)) {
		return []string{"unresolved_template_marker:" + name}
	}
	for _, match := range runnerCrossArtifactMustachePattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		inner := strings.TrimSpace(match[1])
		if !strings.HasPrefix(strings.ToLower(inner), "artifact:") {
			return []string{"unresolved_template_marker:" + name}
		}
		reference := strings.TrimSpace(inner[len("artifact:"):])
		if _, err := uuid.Parse(reference); err != nil {
			return []string{"malformed_artifact_placeholder:" + name + " value=" + reference}
		}
	}
	return nil
}

func runnerCrossArtifactWholeFileScriptTemplate(content string) bool {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	return ((strings.HasPrefix(lower, "<?php") || strings.HasPrefix(lower, "<?=")) &&
		strings.HasSuffix(lower, "?>")) ||
		(strings.HasPrefix(lower, "<%") && strings.HasSuffix(lower, "%>"))
}

// stripRunnerMarkdownMathForTemplateCheck removes only dollar-delimited math
// spans before Python/str.format placeholder detection. LaTeX denominators
// such as `\frac{A}{F}` are final rendered notation, not template fields. The
// Mustache and Jinja checks still run on the original content, so this cannot
// hide an unresolved `{{...}}` or `{%...%}` marker.
func stripRunnerMarkdownMathForTemplateCheck(content string) string {
	var out strings.Builder
	for index := 0; index < len(content); {
		if content[index] != '$' || (index > 0 && content[index-1] == '\\') {
			out.WriteByte(content[index])
			index++
			continue
		}
		delimiter := "$"
		if index+1 < len(content) && content[index+1] == '$' {
			delimiter = "$$"
		}
		end := strings.Index(content[index+len(delimiter):], delimiter)
		if end < 0 {
			out.WriteString(delimiter)
			index += len(delimiter)
			continue
		}
		spanLength := len(delimiter) + end + len(delimiter)
		out.WriteString(strings.Repeat(" ", spanLength))
		index += spanLength
	}
	return out.String()
}

func runnerCrossArtifactHasUnrenderedFormatField(content string) bool {
	for _, location := range runnerCrossArtifactFormatFieldPattern.FindAllStringIndex(content, -1) {
		if location[0] == 0 {
			return true
		}
		previous := content[location[0]-1]
		if (previous >= 'A' && previous <= 'Z') || (previous >= 'a' && previous <= 'z') ||
			(previous >= '0' && previous <= '9') || strings.ContainsRune(`_\\^~`, rune(previous)) {
			continue
		}
		return true
	}
	return false
}

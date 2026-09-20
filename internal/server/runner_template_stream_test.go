package server

import (
	"context"
	"errors"
	"math/rand"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var legacyRunnerCrossArtifactMustachePattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
var legacyRunnerCrossArtifactFormatFieldPattern = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*(?:![rsa])?(?::[^{}\r\n]{1,40})?\}`)

func legacyRunnerCrossArtifactTemplateFailures(name, content string) []string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".md", ".txt", ".html", ".htm", ".csv", ".tsv":
	default:
		return nil
	}
	if legacyRunnerCrossArtifactWholeFileScriptTemplate(content) {
		return []string{"unresolved_template_marker:" + name}
	}
	if strings.Contains(content, "{%") || strings.Contains(content, "%}") {
		return []string{"unresolved_template_marker:" + name}
	}
	// User-facing text is published as final materialized bytes, not a format
	// template. Restrict this check to identifier-shaped Python/str.format
	// fields so mathematical braces, chemical notation, JSON examples, and
	// canonical artifact references remain valid.
	withoutArtifactReferences := legacyRunnerCrossArtifactMustachePattern.ReplaceAllStringFunc(content, func(value string) string {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "{{"), "}}"))
		if strings.HasPrefix(strings.ToLower(inner), "artifact:") {
			return ""
		}
		return value
	})
	if legacyRunnerCrossArtifactHasUnrenderedFormatField(legacyStripRunnerMarkdownMathForTemplateCheck(withoutArtifactReferences)) {
		return []string{"unresolved_template_marker:" + name}
	}
	for _, match := range legacyRunnerCrossArtifactMustachePattern.FindAllStringSubmatch(content, -1) {
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

func legacyRunnerCrossArtifactWholeFileScriptTemplate(content string) bool {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	return ((strings.HasPrefix(lower, "<?php") || strings.HasPrefix(lower, "<?=")) &&
		strings.HasSuffix(lower, "?>")) ||
		(strings.HasPrefix(lower, "<%") && strings.HasSuffix(lower, "%>"))
}

// legacyStripRunnerMarkdownMathForTemplateCheck removes only dollar-delimited math
// spans before Python/str.format placeholder detection. LaTeX denominators
// such as `\frac{A}{F}` are final rendered notation, not template fields. The
// Mustache and Jinja checks still run on the original content, so this cannot
// hide an unresolved `{{...}}` or `{%...%}` marker.
func legacyStripRunnerMarkdownMathForTemplateCheck(content string) string {
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

func legacyRunnerCrossArtifactHasUnrenderedFormatField(content string) bool {
	for _, location := range legacyRunnerCrossArtifactFormatFieldPattern.FindAllStringIndex(content, -1) {
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

// Differential fixtures retain the previous small-document semantics while the
// production implementation changes from whole-file replacement to scanning.
func TestRunnerTemplateStreamMatchesSmallDocumentSemantics(t *testing.T) {
	pieces := []string{"ordinary", " ", "\n", "{", "}", "{{", "}}", "{x}", "{x!r:.2f}", "{x:}", "{x!q}", "$", "$$", "\\\\", "_", "^", "~", "{{ x }}", "{{x.y}}", "{{artifact:bad}}", "{{artifact:123e4567-e89b-12d3-a456-426614174000}}", "{%", "%}", "你好", "é", "<?php", "?>", "<%", "%>", "a{B}", "$\\\\frac{Dose}{CL}$"}
	pieces = append(pieces, "{foo{{artifact:bad}}bar}", "$math{{artifact:bad$}}{x}$", "a{{{artifact:bad}}{x}", "{x:你好}")
	random := rand.New(rand.NewSource(17))
	for sample := 0; sample < 5000; sample++ {
		var text strings.Builder
		for count := random.Intn(12) + 1; count > 0; count-- {
			text.WriteString(pieces[random.Intn(len(pieces))])
		}
		content := text.String()
		want := legacyRunnerCrossArtifactTemplateFailures("report.md", content)
		got, err := scanRunnerTemplateFailures(context.Background(), strings.NewReader(content), "report.md")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("content=%q got=%v want=%v err=%v", content, got, want, err)
		}
	}
}

type shortTemplateReader struct{ *strings.Reader }

func (reader shortTemplateReader) Read(data []byte) (int, error) {
	return reader.Reader.Read(data[:min(len(data), 3)])
}

func TestRunnerTemplateStreamLargeTokensMathAndCancellation(t *testing.T) {
	const reference = "artifact:123e4567-e89b-12d3-a456-426614174000"
	for _, content := range []string{
		"{{" + strings.Repeat(" ", 2<<20) + reference + strings.Repeat("\n", 2<<20) + "}}",
		"$" + strings.Repeat("ordinary math ", 1000) + "\\frac{Dose}{CL}$",
		"{foo{{" + reference + "}}bar}",
		"$math{{artifact:bad$}}{x}$",
		"{{{ x }}",
	} {
		got, err := scanRunnerTemplateFailures(context.Background(), shortTemplateReader{strings.NewReader(content)}, "report.md")
		want := legacyRunnerCrossArtifactTemplateFailures("report.md", content)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("length=%d got=%v want=%v err=%v", len(content), got, want, err)
		}
	}
	got, err := scanRunnerTemplateFailures(context.Background(), strings.NewReader("{{artifact:"+strings.Repeat("x", 2<<20)+"}}"), "report.md")
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "value_sha256=") {
		t.Fatalf("large invalid token=%v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanRunnerTemplateFailures(ctx, strings.NewReader("valid"), "report.md"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

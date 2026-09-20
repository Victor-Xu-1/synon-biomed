package server

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// A different conversion route for a rejected final presentation. Scientific
// literals remain owned by the host, rather than relying on a second copy made
// by the translator. This never creates a tool proposal or a scientific turn.
type responseLanguageTemplate struct {
	text     string
	prefix   string
	literals []string
}

func newResponseLanguageTemplate(original string) responseLanguageTemplate {
	protected := make([]bool, len(original))
	for _, pattern := range responseLanguageLiteralPatterns {
		for _, span := range pattern.FindAllStringIndex(original, -1) {
			end := span[1]
			if strings.HasPrefix(original[span[0]:end], "http://") || strings.HasPrefix(original[span[0]:end], "https://") {
				end = span[0] + len(strings.TrimRight(original[span[0]:end], ".,;:!?"))
			}
			for index := span[0]; index < end; index++ {
				protected[index] = true
			}
		}
	}
	digest := sha256.Sum256([]byte(original))
	template := responseLanguageTemplate{prefix: fmt.Sprintf("⟪literal-%x-", digest[:8])}
	for strings.Contains(original, template.prefix) {
		template.prefix += "_"
	}
	var text strings.Builder
	for start := 0; start < len(original); {
		end := start + 1
		for end < len(original) && protected[end] == protected[start] {
			end++
		}
		if protected[start] {
			text.WriteString(template.marker(len(template.literals)))
			template.literals = append(template.literals, original[start:end])
		} else {
			text.WriteString(original[start:end])
		}
		start = end
	}
	template.text = text.String()
	return template
}

func (template responseLanguageTemplate) marker(index int) string {
	return fmt.Sprintf("%s%d⟫", template.prefix, index)
}

func (template responseLanguageTemplate) restore(translated string) (string, bool) {
	for index := range template.literals {
		if strings.Count(translated, template.marker(index)) != 1 {
			return "", false
		}
	}
	// One replacement pass: a source literal is data, never another marker.
	pairs := make([]string, 0, 2*len(template.literals))
	for index, literal := range template.literals {
		pairs = append(pairs, template.marker(index), literal)
	}
	translated = strings.NewReplacer(pairs...).Replace(translated)
	if strings.Contains(translated, template.prefix) {
		return "", false
	}
	return translated, true
}

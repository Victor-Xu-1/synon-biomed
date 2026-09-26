package server

import (
	"maps"
	"regexp"
	"strings"
)

var responseLanguageLiteralPatterns = []*regexp.Regexp{
	responseLanguageFencedCodePattern,
	responseLanguageInlineCodePattern,
	regexp.MustCompile(`https?://[^\s)>\]，。；！？、]+`),
	regexp.MustCompile(`\{\{artifact:[^{}]+\}\}`),
	regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9_.-]{0,160}\.[a-z][a-z0-9]{0,11}\b`),
	regexp.MustCompile(`\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b`),
	regexp.MustCompile(`\b[A-Z0-9][A-Z0-9_-]{1,}\b`),
}

func responseLanguageProtectedLiterals(text string) map[string]int {
	result := map[string]int{}
	for _, pattern := range responseLanguageLiteralPatterns {
		text = pattern.ReplaceAllStringFunc(text, func(value string) string {
			if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				value = strings.TrimRight(value, ".,;:!?")
			}
			result[value]++
			return " "
		})
	}
	return result
}

func responseLanguageLiteralsPreserved(original, translated string) bool {
	wanted := responseLanguageProtectedLiterals(original)
	actual := responseLanguageProtectedLiterals(translated)
	return maps.Equal(wanted, actual)
}

func sessionRunnerClearlyEnglishProgress(text string) bool {
	// Ignore immutable evidence tokens before classifying short action prose.
	// Two ordinary words such as "Saving results" are a full progress update,
	// while a list of identifiers or filenames alone is not English narration.
	narrative := text
	for _, pattern := range responseLanguageLiteralPatterns {
		narrative = pattern.ReplaceAllString(narrative, " ")
	}
	han, latin, words := sessionRunnerLanguageProfile(narrative)
	return han == 0 && latin >= 8 && words >= 2 || sessionRunnerClearlyEnglishNarrative(text, true) && words > 0
}

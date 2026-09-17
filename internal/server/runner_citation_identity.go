package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"synon-go/internal/agentruntime"
)

const (
	maxRunnerCitationIdentityDepth = 32
	maxRunnerCitationIdentityNodes = 10000
)

type runnerCitationIdentityIndex struct {
	byDOI   map[string]map[string]string
	byTitle map[string]map[string]struct{}
}

func runnerCitationIdentityIndexFromMessages(messages []agentruntime.Message) runnerCitationIdentityIndex {
	index := runnerCitationIdentityIndex{byDOI: make(map[string]map[string]string), byTitle: make(map[string]map[string]struct{})}
	nodes := 0
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > maxRunnerCitationIdentityDepth || nodes >= maxRunnerCitationIdentityNodes {
			return
		}
		nodes++
		switch typed := value.(type) {
		case map[string]any:
			doi := runnerCitationDOI(runnerSourceFirstStringValue(typed, "doi", "DOI", "citation_handle", "citationHandle"))
			if doi == "" {
				for _, raw := range anySliceValue(typed["identifiers"]) {
					identifier := mapValue(raw)
					if strings.EqualFold(strings.TrimSpace(stringValue(identifier["namespace"])), "doi") {
						doi = runnerCitationDOI(stringValue(identifier["value"]))
						break
					}
				}
			}
			title := strings.TrimSpace(runnerSourceFirstStringValue(typed, "title", "article_title", "articleTitle"))
			index.add(doi, title)
			for _, child := range typed {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range typed {
				walk(child, depth+1)
			}
		}
	}
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(message.Content), &value) == nil {
			walk(value, 0)
		}
	}
	return index
}

func (index runnerCitationIdentityIndex) add(doi, title string) {
	doi = runnerCitationDOI(doi)
	normalizedTitle := normalizeRunnerCitationTitle(title)
	if doi == "" || len([]rune(normalizedTitle)) < 8 {
		return
	}
	if index.byDOI[doi] == nil {
		index.byDOI[doi] = make(map[string]string)
	}
	index.byDOI[doi][normalizedTitle] = strings.TrimSpace(title)
	if index.byTitle[normalizedTitle] == nil {
		index.byTitle[normalizedTitle] = make(map[string]struct{})
	}
	index.byTitle[normalizedTitle][doi] = struct{}{}
}

// runnerCitationIdentityFailures reports only a demonstrable record-identity
// conflict: a DOI is printed on the same line as the exact known title of a
// different DOI, while none of that DOI's own known titles is present. A
// translated, shortened or otherwise unknown title remains unjudged.
func runnerCitationIdentityFailures(name string, data []byte, index runnerCitationIdentityIndex) []string {
	if len(index.byDOI) == 0 || len(data) == 0 {
		return nil
	}
	failures := []string{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		normalizedLine := normalizeRunnerCitationTitle(line)
		if normalizedLine == "" {
			continue
		}
		for _, rawDOI := range sessionRunnerDOIPattern.FindAllString(line, -1) {
			doi := runnerCitationDOI(rawDOI)
			expectedTitles := index.byDOI[doi]
			if len(expectedTitles) == 0 {
				continue
			}
			expectedMatched := false
			for title := range expectedTitles {
				if strings.Contains(normalizedLine, title) {
					expectedMatched = true
					break
				}
			}
			if expectedMatched {
				continue
			}
			observed := ""
			for _, title := range runnerCitationSortedTitles(index.byTitle) {
				dois := index.byTitle[title]
				if len([]rune(title)) < 12 || !strings.Contains(normalizedLine, title) {
					continue
				}
				if _, same := dois[doi]; same {
					continue
				}
				for otherDOI := range dois {
					if display := index.byDOI[otherDOI][title]; display != "" {
						observed = display
						break
					}
				}
				if observed != "" {
					break
				}
			}
			if observed == "" {
				continue
			}
			expected := runnerCitationFirstTitle(expectedTitles)
			failure := fmt.Sprintf(
				"citation_identity_conflict:%s doi=%s expected_title=%s observed_title=%s",
				name, doi, expected, observed,
			)
			if _, duplicate := seen[failure]; duplicate {
				continue
			}
			seen[failure] = struct{}{}
			failures = append(failures, failure)
		}
	}
	sort.Strings(failures)
	return failures
}

func runnerCitationSortedTitles(values map[string]map[string]struct{}) []string {
	titles := make([]string, 0, len(values))
	for title := range values {
		titles = append(titles, title)
	}
	sort.Strings(titles)
	return titles
}

func runnerCitationDOI(value string) string {
	canonical := runnerEvidenceCanonicalPublication(value)
	if strings.HasPrefix(canonical, "doi:") {
		return strings.TrimPrefix(canonical, "doi:")
	}
	return ""
}

func runnerCitationFirstTitle(titles map[string]string) string {
	values := make([]string, 0, len(titles))
	for _, title := range titles {
		values = append(values, title)
	}
	sort.Strings(values)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func normalizeRunnerCitationTitle(value string) string {
	var output strings.Builder
	space := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			if space && output.Len() > 0 {
				output.WriteByte(' ')
			}
			output.WriteRune(character)
			space = false
			continue
		}
		space = true
	}
	return strings.TrimSpace(output.String())
}

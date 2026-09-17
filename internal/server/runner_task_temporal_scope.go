package server

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var sessionRunnerChineseAsOfDatePattern = regexp.MustCompile(`(?:截至|截止(?:到|至)?)\s*([0-9]{4})年([0-9]{1,2})月([0-9]{1,2})日`)
var sessionRunnerChineseISOAsOfDatePattern = regexp.MustCompile(`(?:截至|截止(?:到|至)?)\s*([0-9]{4})-([0-9]{2})-([0-9]{2})`)
var sessionRunnerEnglishISOAsOfDatePattern = regexp.MustCompile(`(?i)(?:as\s+of|available\s+through|through|up\s+to)\s*[:：]?\s*([0-9]{4})-([0-9]{2})-([0-9]{2})`)

type sessionRunnerTemporalScope struct {
	Kind       string `json:"kind"`
	Date       string `json:"date"`
	Precision  string `json:"precision"`
	SourceText string `json:"sourceText"`
}

func sessionRunnerTaskTemporalScopes(task string) []sessionRunnerTemporalScope {
	type match struct {
		position int
		scope    sessionRunnerTemporalScope
	}
	matches := []match{}
	for _, pattern := range []*regexp.Regexp{
		sessionRunnerChineseAsOfDatePattern,
		sessionRunnerChineseISOAsOfDatePattern,
		sessionRunnerEnglishISOAsOfDatePattern,
	} {
		for _, location := range pattern.FindAllStringSubmatchIndex(task, -1) {
			if len(location) != 8 {
				continue
			}
			year, yearErr := strconv.Atoi(task[location[2]:location[3]])
			month, monthErr := strconv.Atoi(task[location[4]:location[5]])
			day, dayErr := strconv.Atoi(task[location[6]:location[7]])
			if yearErr != nil || monthErr != nil || dayErr != nil || !validSessionRunnerDate(year, month, day) {
				continue
			}
			matches = append(matches, match{position: location[0], scope: sessionRunnerTemporalScope{
				Kind: "public_information_available_through", Date: fmt.Sprintf("%04d-%02d-%02d", year, month, day),
				Precision: "day", SourceText: strings.TrimSpace(task[location[0]:location[1]]),
			}})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].position < matches[j].position })
	result := make([]sessionRunnerTemporalScope, 0, min(len(matches), 8))
	seen := map[string]struct{}{}
	for _, candidate := range matches {
		key := candidate.scope.Kind + "\x00" + candidate.scope.Date
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate.scope)
		if len(result) == 8 {
			break
		}
	}
	return result
}

func validSessionRunnerDate(year, month, day int) bool {
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	value := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return value.Year() == year && int(value.Month()) == month && value.Day() == day
}

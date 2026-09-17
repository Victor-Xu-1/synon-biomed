package sourcecitation

import (
	"regexp"
	"strings"
)

const MaxCitationTextRunes = 2048

var citationDOIPattern = regexp.MustCompile(`(?i)^10\.[0-9]{4,9}/[-._;()/:a-z0-9]+$`)
var citationPMIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
var citationPMCIDPattern = regexp.MustCompile(`(?i)^PMC[0-9]+$`)

// Input is source metadata already attested by an acquisition tool. Build
// formats that metadata only; it never infers a missing identifier or decides
// whether the record semantically supports a scientific claim.
type Input struct {
	DOI, PMID, PMCID                   string
	Authors, Title, Journal, Published string
}

type Citation struct {
	Handle string
	Text   string
}

func Build(input Input) Citation {
	handle, identifier := "", ""
	for _, candidate := range []struct {
		namespace, value string
	}{
		{"doi", input.DOI}, {"pmid", input.PMID}, {"pmcid", input.PMCID},
	} {
		value := strings.TrimSpace(candidate.value)
		if value == "" || !validCitationIdentifier(candidate.namespace, value) {
			continue
		}
		switch candidate.namespace {
		case "doi":
			value = strings.ToLower(value)
		case "pmcid":
			value = strings.ToUpper(value)
		}
		handle = candidate.namespace + ":" + value
		identifier = strings.ToUpper(candidate.namespace) + ":" + value
		break
	}
	parts := make([]string, 0, 5)
	for _, value := range []string{input.Authors, input.Title, input.Journal, input.Published, identifier} {
		if value = strings.Trim(strings.TrimSpace(value), "."); value != "" {
			parts = append(parts, value)
		}
	}
	text := strings.Join(parts, ". ")
	if text != "" {
		text += "."
	}
	runes := []rune(text)
	if len(runes) > MaxCitationTextRunes {
		text = string(runes[:MaxCitationTextRunes-1]) + "…"
	}
	return Citation{Handle: handle, Text: text}
}

func validCitationIdentifier(namespace, value string) bool {
	switch namespace {
	case "doi":
		return citationDOIPattern.MatchString(value)
	case "pmid":
		return citationPMIDPattern.MatchString(value)
	case "pmcid":
		return citationPMCIDPattern.MatchString(value)
	default:
		return false
	}
}

package websearch

import (
	"fmt"
	"strings"
	"time"

	"synon-go/internal/sourcecitation"
)

// SourceRecord contains metadata the upstream provider actually returned.
// It is not a scientific claim and does not upgrade a search hit to a full
// article read. The complete provider abstract, when present, is retained so
// the bounded search snippet is only a presentation field.
type SourceRecord struct {
	Provider         string             `json:"provider"`
	Title            string             `json:"title,omitempty"`
	Identifiers      []SourceIdentifier `json:"identifiers,omitempty"`
	Publisher        string             `json:"publisher,omitempty"`
	Journal          string             `json:"journal,omitempty"`
	Authors          string             `json:"authors,omitempty"`
	Published        *SourceDate        `json:"published,omitempty"`
	Updated          *SourceDate        `json:"updated,omitempty"`
	Abstract         string             `json:"abstract,omitempty"`
	AbstractComplete bool               `json:"abstract_complete,omitempty"`
	RecordDepth      string             `json:"record_depth"`
	CitationHandle   string             `json:"citation_handle,omitempty"`
	CitationText     string             `json:"citation_text,omitempty"`
}

type SourceIdentifier struct {
	Namespace string `json:"namespace"`
	Value     string `json:"value"`
}

// SourceDate retains the provider value and its actual precision. A year-only
// field must never be presented as a precise publication day.
type SourceDate struct {
	Value       string `json:"value"`
	Precision   string `json:"precision"`
	Raw         string `json:"raw"`
	SourceField string `json:"source_field"`
}

func sourceRecord(provider, title string, identifiers []SourceIdentifier, publisher, journal, authors, abstract string, published *SourceDate) *SourceRecord {
	abstract = normalizeSpace(stripHTML(abstract))
	depth := "metadata_record"
	if abstract != "" {
		depth = "abstract_record"
	}
	record := &SourceRecord{
		Provider: strings.TrimSpace(provider), Identifiers: uniqueSourceIdentifiers(identifiers),
		Title: strings.TrimSpace(title), Publisher: strings.TrimSpace(publisher), Journal: strings.TrimSpace(journal), Authors: strings.TrimSpace(authors),
		Published: published, Abstract: abstract, AbstractComplete: abstract != "", RecordDepth: depth,
	}
	citation := sourcecitation.Build(sourcecitation.Input{
		DOI: sourceRecordIdentifierValue(record, "doi"), PMID: sourceRecordIdentifierValue(record, "pmid"),
		PMCID: sourceRecordIdentifierValue(record, "pmcid"), Authors: record.Authors, Title: record.Title,
		Journal: firstNonEmpty(record.Journal, record.Publisher), Published: sourceRecordDateValue(record.Published),
	})
	record.CitationHandle, record.CitationText = citation.Handle, citation.Text
	return record
}

func sourceRecordIdentifierValue(record *SourceRecord, namespace string) string {
	if record == nil {
		return ""
	}
	for _, identifier := range record.Identifiers {
		if strings.EqualFold(identifier.Namespace, namespace) {
			return strings.TrimSpace(identifier.Value)
		}
	}
	return ""
}

func sourceRecordDateValue(value *SourceDate) string {
	if value == nil {
		return ""
	}
	return value.Value
}

func sourceIdentifier(namespace, value string) SourceIdentifier {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	value = strings.TrimSpace(value)
	switch namespace {
	case "doi":
		value = strings.ToLower(value)
	case "pmcid":
		value = strings.ToUpper(value)
	}
	return SourceIdentifier{Namespace: namespace, Value: value}
}

func uniqueSourceIdentifiers(values []SourceIdentifier) []SourceIdentifier {
	result := make([]SourceIdentifier, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = sourceIdentifier(value.Namespace, value.Value)
		if value.Namespace == "" || value.Value == "" {
			continue
		}
		key := value.Namespace + "\x00" + strings.ToLower(value.Value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func sourceRecordHasIdentifier(record *SourceRecord, namespace, value string) bool {
	if record == nil {
		return false
	}
	want := sourceIdentifier(namespace, value)
	for _, identifier := range record.Identifiers {
		got := sourceIdentifier(identifier.Namespace, identifier.Value)
		if got.Namespace == want.Namespace && strings.EqualFold(got.Value, want.Value) {
			return true
		}
	}
	return false
}

func sourceDateFromParts(parts [][]int, sourceField string) *SourceDate {
	if len(parts) == 0 || len(parts[0]) == 0 {
		return nil
	}
	values := parts[0]
	if values[0] < 1 || values[0] > 9999 {
		return nil
	}
	value := fmt.Sprintf("%04d", values[0])
	precision := "year"
	if len(values) >= 2 && values[1] >= 1 && values[1] <= 12 {
		value = fmt.Sprintf("%04d-%02d", values[0], values[1])
		precision = "month"
		if len(values) >= 3 && validCalendarDate(values[0], values[1], values[2]) {
			value = fmt.Sprintf("%04d-%02d-%02d", values[0], values[1], values[2])
			precision = "day"
		}
	}
	rawValues := make([]string, 0, len(values))
	for _, part := range values {
		rawValues = append(rawValues, fmt.Sprintf("%d", part))
	}
	return &SourceDate{Value: value, Precision: precision, Raw: strings.Join(rawValues, "-"), SourceField: sourceField}
}

func sourceDateFromText(value, sourceField string) *SourceDate {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil
	}
	for _, candidate := range []struct {
		layout, precision string
	}{
		{"2006-01-02", "day"}, {"20060102", "day"}, {"2006-01", "month"}, {"2006", "year"},
	} {
		parsed, err := time.Parse(candidate.layout, raw)
		if err != nil {
			continue
		}
		formatted := parsed.Format(candidate.layout)
		if candidate.layout == "20060102" {
			formatted = parsed.Format("2006-01-02")
		}
		return &SourceDate{Value: formatted, Precision: candidate.precision, Raw: raw, SourceField: sourceField}
	}
	return &SourceDate{Value: raw, Precision: "unknown", Raw: raw, SourceField: sourceField}
}

func validCalendarDate(year, month, day int) bool {
	if day < 1 || day > 31 {
		return false
	}
	parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return parsed.Year() == year && int(parsed.Month()) == month && parsed.Day() == day
}

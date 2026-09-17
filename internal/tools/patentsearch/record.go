package patentsearch

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultPatentSectionChars = 32 * 1024
	maximumPatentSectionChars = 128 * 1024
)

var (
	patentClaimPattern                = regexp.MustCompile(`(?is)<claim\b[^>]*\bnum=["']([^"']+)["'][^>]*>(.*?)</claim>`)
	patentClaimTextPattern            = regexp.MustCompile(`(?is)<div\b[^>]*\bclass=["'][^"']*\bclaim-text\b[^"']*["'][^>]*>(.*?)</div>`)
	patentClaimNumberPattern          = regexp.MustCompile(`(?is)^\s*<b>\s*([0-9]+)\s*</b>`)
	patentParagraphStartPattern       = regexp.MustCompile(`(?is)<div\b[^>]*\bid=["'](p[0-9]+)["'][^>]*\bclass=["'][^"']*description-paragraph[^"']*["'][^>]*>`)
	patentDescriptionLineStartPattern = regexp.MustCompile(`(?is)<div\b[^>]*\bid=["']([^"']+)["'][^>]*\bclass=["'][^"']*\bdescription-line\b[^"']*["'][^>]*>`)
	patentTranslatedParagraphPattern  = regexp.MustCompile(`(?is)<p\b[^>]*\bid=["'](p[0-9]+)["'][^>]*>`)
)

func patentLookupEvidenceDepth(records []Record) string {
	for _, record := range records {
		if record.RecordDepth == "full_record" && record.EvidenceState == "record-read" {
			return "full_record"
		}
	}
	return "locator_only"
}

func (client *Client) lookupRecords(
	ctx context.Context,
	publicationNumber string,
	selected []sourceSpec,
	input Input,
	warnings []string,
) ([]Record, []string) {
	records := make([]Record, 0, len(selected))
	for _, spec := range selected {
		record := Record{
			Source: spec.id, SourceLabel: spec.label, Title: publicationNumber,
			URL: spec.lookupURL(publicationNumber), PublicationNumber: publicationNumber,
			EvidenceState: "official-record-location", RecordDepth: "locator",
		}
		if spec.id != "google_patents" {
			records = append(records, record)
			continue
		}
		body, err := client.fetchHTML(ctx, record.URL)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Google Patents record could not be read: %v", err))
			records = append(records, record)
			continue
		}
		record = parseGooglePatentRecord(body, record, input.FocusTerms, input.MaxSectionChars)
		if record.RecordDepth != "full_record" {
			warnings = append(warnings, "Google Patents page did not expose readable abstract, claims, or description sections.")
		}
		records = append(records, record)
	}
	return records, warnings
}

func parseGooglePatentRecord(document string, record Record, focusTerms []string, maxSectionChars int) Record {
	limit := maxSectionChars
	if limit <= 0 {
		limit = defaultPatentSectionChars
	}
	if limit > maximumPatentSectionChars {
		limit = maximumPatentSectionChars
	}

	if value := firstItempropText(document, "title"); value != "" {
		record.Title = value
	}
	record.Inventors = itempropTexts(document, "inventor")
	record.Assignees = deduplicateStrings(append(itempropTexts(document, "assigneeCurrent"), itempropTexts(document, "assigneeOriginal")...))
	record.PriorityDate = firstItempropDate(document, "priorityDate")
	record.PublicationDate = firstItempropDate(document, "publicationDate")
	record.LegalStatus = firstItempropText(document, "status")
	record.Abstract = boundedText(sectionText(document, "abstract"), limit/4)
	record.Claims = parsePatentClaims(sectionHTML(document, "claims"), limit/2)
	description := parseDescriptionParagraphs(sectionHTML(document, "description"))
	record.DescriptionExcerpts = selectDescriptionPassages(description, focusTerms, limit/3)
	record.ExampleExcerpts = selectExamplePassages(description, limit/4)
	record.SectionsRead = patentRecordSectionsRead(record)
	record.MatchedFocusTerms = patentRecordMatchedFocusTerms(record, focusTerms)
	record.FocusRelevant = len(normalizedFocusTerms(focusTerms)) == 0 ||
		len(record.MatchedFocusTerms) == len(normalizedFocusTerms(focusTerms))
	// Some offices expose the full enforceable claims and abstract while the
	// translated description markup is unavailable. Abstract plus claims is a
	// substantive claim-level record read and must not be demoted to a locator;
	// an abstract without claims remains partial discovery evidence.
	if len(record.Claims) > 0 && (len(record.DescriptionExcerpts) > 0 || strings.TrimSpace(record.Abstract) != "") {
		record.EvidenceState = "record-read"
		record.RecordDepth = "full_record"
	} else if len(record.SectionsRead) > 0 {
		record.EvidenceState = "record-partially-read"
		record.RecordDepth = "partial_record"
	}
	return record
}

func patentRecordMatchedFocusTerms(record Record, focusTerms []string) []string {
	terms := normalizedFocusTerms(focusTerms)
	if len(terms) == 0 {
		return nil
	}
	var content strings.Builder
	content.WriteString(record.Title)
	content.WriteByte('\n')
	content.WriteString(record.Abstract)
	for _, claim := range record.Claims {
		content.WriteByte('\n')
		content.WriteString(claim.Text)
	}
	for _, passages := range [][]Passage{record.DescriptionExcerpts, record.ExampleExcerpts} {
		for _, passage := range passages {
			content.WriteByte('\n')
			content.WriteString(passage.Text)
		}
	}
	lower := strings.ToLower(content.String())
	matched := make([]string, 0, len(terms))
	for _, term := range terms {
		if strings.Contains(lower, term) {
			matched = append(matched, term)
		}
	}
	return matched
}

func sectionHTML(document, itemprop string) string {
	opening := regexp.MustCompile(`(?is)<section\b[^>]*\bitemprop=["']` + regexp.QuoteMeta(itemprop) + `["'][^>]*>`)
	match := opening.FindStringIndex(document)
	if len(match) != 2 {
		return ""
	}
	sectionEnd := strings.Index(document[match[1]:], "</section>")
	if sectionEnd < 0 {
		return ""
	}
	return document[match[0] : match[1]+sectionEnd+len("</section>")]
}

func sectionText(document, itemprop string) string {
	return cleanPatentRecordText(sectionHTML(document, itemprop))
}

func firstItempropText(document, itemprop string) string {
	values := itempropTexts(document, itemprop)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func itempropTexts(document, itemprop string) []string {
	pattern := regexp.MustCompile(`(?is)<(?:span|dd|div)\b[^>]*\bitemprop=["']` + regexp.QuoteMeta(itemprop) + `["'][^>]*>(.*?)</(?:span|dd|div)>`)
	matches := pattern.FindAllStringSubmatch(document, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		if value := cleanPatentRecordText(match[1]); value != "" {
			values = append(values, value)
		}
	}
	return deduplicateStrings(values)
}

func firstItempropDate(document, itemprop string) string {
	pattern := regexp.MustCompile(`(?is)<time\b[^>]*\bitemprop=["']` + regexp.QuoteMeta(itemprop) + `["'][^>]*\bdatetime=["']([^"']+)["']`)
	match := pattern.FindStringSubmatch(document)
	if len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func parsePatentClaims(section string, charLimit int) []Claim {
	matches := patentClaimPattern.FindAllStringSubmatch(section, -1)
	claims := make([]Claim, 0, len(matches)+8)
	used := 0
	for _, match := range matches {
		text := cleanPatentRecordText(match[2])
		if text == "" {
			continue
		}
		if used+len(text) > charLimit && len(claims) > 0 {
			break
		}
		lower := strings.ToLower(text)
		dependent := strings.Contains(lower, "according to claim") ||
			strings.Contains(lower, "according to any") || strings.Contains(lower, "of claim ") ||
			strings.Contains(lower, "前述权利要求") || strings.Contains(lower, "根据权利要求")
		claims = append(claims, Claim{Number: strings.TrimSpace(match[1]), Text: boundedText(text, charLimit-used), Independent: !dependent})
		used += len(text)
	}
	if len(matches) > 0 {
		return claims
	}
	for index, match := range patentClaimTextPattern.FindAllStringSubmatch(section, -1) {
		text := cleanPatentRecordText(match[1])
		if text == "" || used+len(text) > charLimit && len(claims) > 0 {
			continue
		}
		number := fmt.Sprintf("%d", index+1)
		if value := patentClaimNumberPattern.FindStringSubmatch(match[1]); len(value) == 2 {
			number = strings.TrimSpace(value[1])
		}
		lower := strings.ToLower(text)
		dependent := strings.Contains(lower, "according to claim") ||
			strings.Contains(lower, "according to any") || strings.Contains(lower, "of claim ") ||
			strings.Contains(lower, "前述权利要求") || strings.Contains(lower, "根据权利要求")
		claims = append(claims, Claim{Number: number, Text: boundedText(text, charLimit-used), Independent: !dependent})
		used += len(text)
	}
	return claims
}

func parseDescriptionParagraphs(section string) []Passage {
	starts := patentParagraphStartPattern.FindAllStringSubmatchIndex(section, -1)
	if len(starts) == 0 {
		starts = patentDescriptionLineStartPattern.FindAllStringSubmatchIndex(section, -1)
	}
	if len(starts) == 0 {
		starts = patentTranslatedParagraphPattern.FindAllStringSubmatchIndex(section, -1)
	}
	passages := make([]Passage, 0, len(starts))
	for index, match := range starts {
		contentStart := match[1]
		contentEnd := len(section)
		if index+1 < len(starts) {
			contentEnd = starts[index+1][0]
		}
		locator := section[match[2]:match[3]]
		if value := cleanPatentRecordText(section[contentStart:contentEnd]); value != "" {
			passages = append(passages, Passage{Locator: locator, Text: value})
		}
	}
	return passages
}

func patentRecordSectionsRead(record Record) []string {
	sections := make([]string, 0, 4)
	if strings.TrimSpace(record.Abstract) != "" {
		sections = append(sections, "abstract")
	}
	if len(record.Claims) > 0 {
		sections = append(sections, "claims")
	}
	if len(record.DescriptionExcerpts) > 0 {
		sections = append(sections, "description")
	}
	if len(record.ExampleExcerpts) > 0 {
		sections = append(sections, "examples")
	}
	return sections
}

func selectDescriptionPassages(passages []Passage, focusTerms []string, charLimit int) []Passage {
	terms := normalizedFocusTerms(focusTerms)
	selected := make([]Passage, 0, 16)
	seen := map[string]bool{}
	appendPassage := func(p Passage) {
		if seen[p.Locator] || passageChars(selected)+len(p.Text) > charLimit {
			return
		}
		seen[p.Locator] = true
		selected = append(selected, p)
	}
	if len(terms) > 0 {
		for _, passage := range passages {
			lower := strings.ToLower(passage.Text)
			for _, term := range terms {
				if strings.Contains(lower, term) {
					appendPassage(passage)
					break
				}
			}
		}
	}
	for _, passage := range passages {
		lower := strings.ToLower(passage.Text)
		if strings.Contains(lower, "summary of the invention") || strings.Contains(lower, "发明内容") || strings.Contains(lower, "technical field") {
			appendPassage(passage)
		}
	}
	for _, passage := range passages {
		if len(selected) >= 8 {
			break
		}
		appendPassage(passage)
	}
	return selected
}

func selectExamplePassages(passages []Passage, charLimit int) []Passage {
	selected := make([]Passage, 0, 12)
	seen := map[string]bool{}
	for index, passage := range passages {
		lower := strings.ToLower(passage.Text)
		if !strings.Contains(lower, "example") && !strings.Contains(lower, "experimental") &&
			!strings.Contains(lower, "实施例") && !strings.Contains(lower, "实验例") {
			continue
		}
		for offset := 0; offset < 3 && index+offset < len(passages); offset++ {
			candidate := passages[index+offset]
			if seen[candidate.Locator] {
				continue
			}
			if passageChars(selected)+len(candidate.Text) > charLimit {
				return selected
			}
			seen[candidate.Locator] = true
			selected = append(selected, candidate)
		}
	}
	return selected
}

func normalizedFocusTerms(values []string) []string {
	terms := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if len([]rune(value)) >= 2 {
			terms = append(terms, value)
		}
	}
	sort.Strings(terms)
	return deduplicateStrings(terms)
}

func passageChars(values []Passage) int {
	total := 0
	for _, value := range values {
		total += len(value.Text)
	}
	return total
}

func cleanPatentRecordText(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	return strings.Join(strings.Fields(value), " ")
}

func boundedText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit < 16 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-1]) + "…"
}

func deduplicateStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

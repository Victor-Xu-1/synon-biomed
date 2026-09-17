package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/discoveryquery"
	"synon-go/internal/httptext"
	"synon-go/internal/sourcecitation"
)

const (
	// Evidence identities remain available for every qualified document in the
	// durable source result. This is only a defensive metadata boundary; the
	// complete immutable result remains authoritative behind ResultReference.
	maxResearchEvidenceCardsPerReceipt = 128
	maxResearchEvidenceCardRunes       = 3200
	maxResearchEvidenceCardPassages    = 4
	maxResearchEvidenceCardTitleRunes  = 512
	maxResearchEvidenceCardURLRunes    = 4096
	maxResearchEvidenceCardFieldRunes  = 2048
)

// researchMaterialEvidenceCards carries bounded, source-anchored evidence
// across transcript compaction. The full immutable tool result remains the
// authority behind ResultReference; these cards prevent later synthesis from
// seeing only opaque handles and falling back to generic model prose.
func researchMaterialEvidenceCards(call agentruntime.ToolCall, result any, investigationFocuses ...string) []map[string]any {
	object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
	if object == nil {
		return nil
	}
	cards := make([]map[string]any, 0, maxResearchEvidenceCardsPerReceipt)
	seen := make(map[string]struct{})
	defaultFocus := firstNonEmpty(
		researchEvidenceCardFocus(call),
		strings.Join(uniqueStrings(investigationFocuses), " "),
	)
	appendDocument := func(document map[string]any) {
		if len(cards) >= maxResearchEvidenceCardsPerReceipt || len(document) == 0 {
			return
		}
		readReceipt := mapValue(document["readReceipt"])
		if relevant, recorded := readReceipt["queryRelevant"]; recorded && !boolValue(relevant, false) {
			return
		}
		sourceURL := boundedResearchEvidenceCardText(firstNonEmpty(
			strings.TrimSpace(stringValue(document["sourceLocator"])),
			strings.TrimSpace(stringValue(document["url"])),
		), maxResearchEvidenceCardURLRunes)
		title := boundedResearchEvidenceCardText(stringValue(document["title"]), maxResearchEvidenceCardTitleRunes)
		identity := canonicalWebResearchURL(sourceURL)
		if identity == "" {
			identity = strings.ToLower(strings.TrimSpace(stringValue(document["source_identifier"])))
		}
		if identity == "" {
			identity = strings.ToLower(title)
		}
		if identity == "" {
			return
		}
		if _, duplicate := seen[identity]; duplicate {
			return
		}
		focus := boundedResearchEvidenceCardText(firstNonEmpty(
			strings.TrimSpace(stringValue(mapValue(document["discovery"])["matchedQueryVariant"])),
			defaultFocus,
		), maxResearchEvidenceCardFieldRunes)
		excerpt := researchEvidenceCardExcerpt(focus, firstNonEmpty(
			stringValue(document["content"]), stringValue(document["body"]), stringValue(document["text"]),
		))
		if excerpt == "" && title == "" && sourceURL == "" {
			return
		}
		seen[identity] = struct{}{}
		card := map[string]any{}
		if excerpt != "" {
			card["excerpt"] = excerpt
			digest := sha256.Sum256([]byte(excerpt))
			card["excerpt_sha256"] = hex.EncodeToString(digest[:])
		}
		if focus != "" {
			card["research_focus"] = focus
		}
		if title != "" {
			card["title"] = title
		}
		if sourceURL != "" {
			card["url"] = sourceURL
		}
		for _, key := range []string{
			"citation_handle", "citation_text", "record_depth", "published_at", "source_locator", "excerpt_scope",
			"source_identifier", "source_status",
		} {
			if value := boundedResearchEvidenceCardText(stringValue(document[key]), researchEvidenceCardFieldLimit(key)); value != "" {
				card[key] = value
			}
		}
		cards = append(cards, card)
	}
	for _, raw := range anySliceValue(object["documents"]) {
		appendDocument(mapValue(raw))
	}
	normalizedTool := normalizeAgentToolName(call.Name)
	if normalizedTool == "websearch" {
		for sourceIndex, rawSource := range anySliceValue(object["sources"]) {
			source := mapValue(rawSource)
			record := mapValue(source["record"])
			content := strings.TrimSpace(stringValue(record["abstract"]))
			recordDepth := strings.TrimSpace(stringValue(record["record_depth"]))
			sourceLocator := fmt.Sprintf("/result/sources/%d/record/abstract", sourceIndex)
			if recordDepth != "abstract_record" || !boolValue(record["abstract_complete"], false) || content == "" {
				content = strings.TrimSpace(stringValue(source["snippet"]))
				sourceLocator = fmt.Sprintf("/result/sources/%d/snippet", sourceIndex)
				if recordDepth == "" {
					recordDepth = "search_snippet"
				}
			}
			published := stringValue(mapValue(record["published"])["value"])
			appendDocument(map[string]any{
				"title": source["title"], "url": source["url"], "content": content,
				"citation_handle": record["citation_handle"], "citation_text": record["citation_text"],
				"record_depth": recordDepth, "published_at": published,
				"source_locator": sourceLocator,
				"excerpt_scope":  "tool-result-json-pointer",
			})
		}
		return cards
	}
	if normalizedTool == "fetcharticlefulltext" {
		body := stringValue(object["body"])
		content := stringValue(object["abstractText"])
		if strings.Contains(strings.ToLower(stringValue(object["contentType"])), "xml") && strings.TrimSpace(body) != "" {
			if document, err := httptext.JATSDocument(context.Background(), body); err == nil && strings.TrimSpace(document.Text) != "" {
				content = document.Text
			} else if strings.TrimSpace(content) == "" {
				content = body
			}
		} else if strings.TrimSpace(body) != "" {
			content = body
		}
		citation := sourcecitation.Build(sourcecitation.Input{
			DOI: stringValue(object["doi"]), PMID: stringValue(object["pmid"]), PMCID: stringValue(object["pmcid"]),
			Authors: stringValue(object["authorString"]), Title: stringValue(object["title"]),
			Journal: stringValue(object["journalTitle"]), Published: stringValue(object["publicationDate"]),
		})
		excerptScope := "tool-result-json-pointer"
		sourceLocator := "/result/abstractText"
		if strings.TrimSpace(body) != "" {
			sourceLocator = "/result/body"
			if strings.Contains(strings.ToLower(stringValue(object["contentType"])), "xml") {
				excerptScope = "jats-readable-projection"
			}
		}
		appendDocument(map[string]any{
			"title": object["title"], "url": object["sourceUrl"], "content": content,
			"citation_handle": firstNonEmpty(stringValue(object["citation_handle"]), citation.Handle),
			"citation_text":   firstNonEmpty(stringValue(object["citation_text"]), citation.Text),
			"record_depth":    object["recordDepth"], "published_at": object["publicationDate"],
			"source_locator": sourceLocator, "excerpt_scope": excerptScope,
		})
		return cards
	}
	for _, field := range []string{"records", "entries", "items", "results"} {
		for recordIndex, rawRecord := range anySliceValue(object[field]) {
			record := mapValue(rawRecord)
			if len(record) == 0 {
				continue
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				continue
			}
			identifier := firstNonEmptyMapString(record,
				"accession", "identifier", "publication_number", "doi", "pmcid", "pmid", "nct_id", "id",
			)
			title := firstNonEmptyMapString(record, "title", "name", "label", "description")
			if title == "" {
				title = identifier
			}
			appendDocument(map[string]any{
				"title":             title,
				"url":               firstNonEmptyMapString(record, "url", "source_url", "sourceUrl", "link", "href"),
				"content":           string(encoded),
				"source_identifier": identifier,
				"source_status":     firstNonEmptyMapString(record, "status", "state", "availability"),
				"record_depth":      firstNonEmpty(firstNonEmptyMapString(record, "record_depth", "recordDepth", "evidence_depth"), "structured_record"),
				"source_locator":    fmt.Sprintf("/%s/%d", field, recordIndex),
				"excerpt_scope":     "normalized-result-json-pointer",
			})
		}
	}
	if len(cards) > 0 || normalizedTool != "webfetch" {
		return cards
	}
	body := firstNonEmpty(stringValue(object["body"]), stringValue(object["content"]), stringValue(object["text"]))
	readable := webResearchReadableDocument(body, stringValue(object["contentType"]))
	appendDocument(map[string]any{
		"title": object["title"], "url": firstNonEmpty(stringValue(object["url"]), researchToolCallURL(call)),
		"content": readable, "source_locator": "/result/body", "excerpt_scope": "html-readable-projection",
	})
	return cards
}

func firstNonEmptyMapString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if current := strings.TrimSpace(stringValue(value[key])); current != "" {
			return current
		}
	}
	return ""
}

func researchEvidenceCardFieldLimit(key string) int {
	switch key {
	case "citation_text":
		return maxResearchEvidenceCardFieldRunes
	case "source_locator":
		return maxResearchEvidenceCardURLRunes
	case "citation_handle", "source_identifier", "source_status":
		return maxResearchEvidenceCardTitleRunes
	default:
		return 256
	}
}

func boundedResearchEvidenceCardText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}

func researchToolCallURL(call agentruntime.ToolCall) string {
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) != nil {
		return ""
	}
	return strings.TrimSpace(stringValue(input["url"]))
}

func researchEvidenceCardFocus(call agentruntime.ToolCall) string {
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) != nil {
		return ""
	}
	values := []string{
		strings.TrimSpace(stringValue(input["query"])),
		strings.TrimSpace(stringValue(input["prompt"])),
	}
	values = append(values, stringValueSlice(input["query_variants"])...)
	return strings.Join(uniqueStrings(values), " ")
}

// researchEvidenceCardExcerpt selects the most query-relevant passages from a
// qualified document instead of copying the page header. The ranking is fully
// generic: it uses the same multilingual discovery vocabulary as Skill and
// tool routing and contains no domain, source, or report-template rules.
func researchEvidenceCardExcerpt(query, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	passages := researchEvidencePassages(value)
	if len(passages) == 0 {
		return researchEvidenceCardExcerptLimit(value)
	}
	type rankedPassage struct {
		index   int
		text    string
		score   int
		matched int
	}
	ranked := make([]rankedPassage, 0, len(passages))
	terms := uniqueStrings(discoveryquery.Terms(query))
	for index, passage := range passages {
		lower := strings.ToLower(passage)
		matched := 0
		for _, raw := range terms {
			term := strings.ToLower(strings.TrimSpace(raw))
			if term == "" || webResearchNonSemanticQueryTerms[term] {
				continue
			}
			if strings.Contains(lower, term) {
				matched++
			}
		}
		// Prefer passages that cover more of the current research focus; use
		// bounded information density and document order only as tie-breakers.
		density := min(len([]rune(passage)), 600) / 60
		ranked = append(ranked, rankedPassage{index: index, text: passage, score: matched*100 + density, matched: matched})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].index < ranked[j].index
	})
	selected := make([]rankedPassage, 0, maxResearchEvidenceCardPassages)
	for _, passage := range ranked {
		if ranked[0].matched > 0 && passage.matched == 0 {
			continue
		}
		selected = append(selected, passage)
		if len(selected) == maxResearchEvidenceCardPassages {
			break
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].index < selected[j].index })
	parts := make([]string, 0, len(selected))
	for _, passage := range selected {
		parts = append(parts, passage.text)
	}
	return researchEvidenceCardExcerptLimit(strings.Join(parts, "\n"))
}

func researchEvidencePassages(value string) []string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	heading := ""
	tableHeader := ""
	for _, rawLine := range lines {
		line := normalizeResearchEvidenceLine(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading = line
			tableHeader = ""
			continue
		}
		if len([]rune(line)) < 24 && !strings.Contains(line, "\t") {
			continue
		}
		passage := line
		if strings.Contains(line, "\t") {
			if tableHeader == "" {
				tableHeader = line
			} else if line != tableHeader {
				passage = tableHeader + "\n" + line
			}
		} else {
			tableHeader = ""
		}
		if heading != "" {
			passage = heading + "\n" + passage
		}
		result = append(result, passage)
	}
	return result
}

func normalizeResearchEvidenceLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "\t") {
		return strings.Join(strings.Fields(value), " ")
	}
	cells := strings.Split(value, "\t")
	for index := range cells {
		cells[index] = strings.Join(strings.Fields(cells[index]), " ")
	}
	return strings.Trim(strings.Join(cells, "\t"), "\t ")
}

func researchEvidenceCardExcerptLimit(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxResearchEvidenceCardRunes {
		runes = runes[:maxResearchEvidenceCardRunes]
	}
	return strings.TrimSpace(string(runes))
}

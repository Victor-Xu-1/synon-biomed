package server

import (
	"context"
	"encoding/json"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/httptext"
)

// researchArticleRecordRelevantToInvestigation applies the same bounded,
// provider-neutral entity/topic check used for generic web reads to structured
// article records. A real DOI can still name the wrong paper; source existence
// alone must not satisfy an unrelated deep-research module.
func researchArticleRecordRelevantToInvestigation(
	call agentruntime.ToolCall,
	result any,
	focuses []string,
) bool {
	object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
	if object == nil {
		return false
	}
	parts := []string{
		strings.TrimSpace(stringValue(object["title"])),
		strings.TrimSpace(stringValue(object["abstractText"])),
		strings.TrimSpace(stringValue(object["abstract"])),
	}
	body := strings.TrimSpace(stringValue(object["body"]))
	contentType := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringValue(object["contentType"]), stringValue(object["content_type"]),
	)))
	if body != "" {
		if strings.Contains(contentType, "xml") {
			if document, err := httptext.JATSDocument(context.Background(), body); err == nil {
				parts = append(parts, document.Text)
			}
		} else {
			parts = append(parts, webResearchReadableDocument(body, contentType))
		}
	}
	readable := strings.TrimSpace(strings.Join(parts, "\n"))
	if readable == "" {
		return false
	}
	if compared, relevant := researchDocumentMatchesAnyFocus(focuses, readable); compared {
		return relevant
	}
	var input map[string]any
	_ = json.Unmarshal(call.Arguments, &input)
	if compared, relevant := researchDocumentMatchesAnyFocus([]string{
		strings.TrimSpace(stringValue(input["prompt"])),
		strings.TrimSpace(stringValue(input["query"])),
	}, readable); compared {
		return relevant
	}
	return true
}

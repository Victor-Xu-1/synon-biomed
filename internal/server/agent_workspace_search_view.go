package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const agentWorkspaceSearchDisplayLineRunes = 800

// The normalized discovery record is the source authority. Its legacy
// results array and ranking/trace metadata need not occupy a second copy in
// every reading page. No records are ranked, filtered or summarized here.
func agentWorkspaceSearchView(ctx context.Context, raw []byte, filename string, size int64, input map[string]any) (map[string]any, bool, error) {
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Query   string `json:"query"`
			Sources []struct {
				Kind    string `json:"kind"`
				State   string `json:"evidenceState"`
				Title   string `json:"title"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
				Record  *struct {
					Provider         string `json:"provider"`
					Publisher        string `json:"publisher"`
					Journal          string `json:"journal"`
					Authors          string `json:"authors"`
					Abstract         string `json:"abstract"`
					AbstractComplete bool   `json:"abstract_complete"`
					RecordDepth      string `json:"record_depth"`
					CitationHandle   string `json:"citation_handle"`
					CitationText     string `json:"citation_text"`
					Identifiers      []struct {
						Namespace string `json:"namespace"`
						Value     string `json:"value"`
					} `json:"identifiers"`
					Published *struct {
						Value       string `json:"value"`
						Precision   string `json:"precision"`
						SourceField string `json:"source_field"`
					} `json:"published"`
				} `json:"record"`
			} `json:"sources"`
			Retrieval   json.RawMessage `json:"retrieval"`
			Diagnostics json.RawMessage `json:"diagnostics"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !envelope.OK || envelope.Result.Query == "" || len(envelope.Result.Sources) == 0 {
		return nil, false, nil
	}
	for _, source := range envelope.Result.Sources {
		if source.Kind != "search_result" || source.State != "discovered" || source.URL == "" {
			return nil, false, nil
		}
	}
	var text strings.Builder
	abstractRecords := 0
	fmt.Fprintf(&text, "Query: %s\n", envelope.Result.Query)
	if len(envelope.Result.Retrieval) > 0 {
		fmt.Fprintf(&text, "Retrieval: %s\n", envelope.Result.Retrieval)
	}
	for i, source := range envelope.Result.Sources {
		fmt.Fprintf(&text, "\n%d. %s\n%s\n%s\n", i+1, source.Title, source.URL, source.Snippet)
		if record := source.Record; record != nil {
			writeAgentWorkspaceDisplayField(&text, "Provider", record.Provider)
			writeAgentWorkspaceDisplayField(&text, "Record depth", record.RecordDepth)
			if record.Published != nil && strings.TrimSpace(record.Published.Value) != "" {
				date := strings.TrimSpace(record.Published.Value)
				details := []string{}
				if value := strings.TrimSpace(record.Published.Precision); value != "" {
					details = append(details, value)
				}
				if value := strings.TrimSpace(record.Published.SourceField); value != "" {
					details = append(details, value)
				}
				if len(details) > 0 {
					date += " (" + strings.Join(details, "; ") + ")"
				}
				writeAgentWorkspaceDisplayField(&text, "Published", date)
			}
			for _, identifier := range record.Identifiers {
				if namespace, value := strings.TrimSpace(identifier.Namespace), strings.TrimSpace(identifier.Value); namespace != "" && value != "" {
					writeAgentWorkspaceDisplayField(&text, "Identifier", namespace+":"+value)
				}
			}
			writeAgentWorkspaceDisplayField(&text, "Publisher", record.Publisher)
			writeAgentWorkspaceDisplayField(&text, "Journal", record.Journal)
			writeAgentWorkspaceDisplayField(&text, "Authors", record.Authors)
			writeAgentWorkspaceDisplayField(&text, "Citation handle", record.CitationHandle)
			writeAgentWorkspaceDisplayField(&text, "Citation", record.CitationText)
			if strings.TrimSpace(record.Abstract) != "" {
				abstractRecords++
				writeAgentWorkspaceDisplayField(&text, "Abstract", record.Abstract)
			}
		}
		fmt.Fprintf(&text, "json_pointer: /result/sources/%d\n", i)
	}
	if len(envelope.Result.Diagnostics) > 0 {
		fmt.Fprintf(&text, "\nDiagnostics: %s\n", envelope.Result.Diagnostics)
	}
	metadata := map[string]any{
		"view_format": "search-results-display-lines", "source_state": "discovered",
		"source_size_bytes": size, "source_count": len(envelope.Result.Sources),
		"source_records_with_abstract": abstractRecords, "source_content_included": abstractRecords > 0,
		"raw_read_with": agentWorkspaceSourceReadInput(input, "/result/sources"),
	}
	result, err := readAgentWorkspaceDocumentPage(ctx, text.String(), filename, size, input, metadata)
	return result, true, err
}

func writeAgentWorkspaceDisplayField(output *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if output == nil || value == "" {
		return
	}
	runes := []rune(value)
	for offset := 0; offset < len(runes); offset += agentWorkspaceSearchDisplayLineRunes {
		end := min(offset+agentWorkspaceSearchDisplayLineRunes, len(runes))
		if offset == 0 {
			fmt.Fprintf(output, "%s: %s\n", label, string(runes[offset:end]))
		} else {
			fmt.Fprintf(output, "%s (continued): %s\n", label, string(runes[offset:end]))
		}
	}
}

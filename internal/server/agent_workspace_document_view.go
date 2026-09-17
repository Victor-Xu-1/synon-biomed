package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"strings"

	"synon-go/internal/httptext"
	"synon-go/internal/sourcecitation"
	"synon-go/internal/tools/webfetch"
)

// Six bounded search providers can together exceed the former eight-MiB JSON
// view ceiling even though every individual response honors its four-MiB cap.
// Keep one explicit aggregate ceiling large enough for that canonical result,
// while still bounding parsing and first-feedback memory.
const maxAgentWorkspaceStructuredJSONBytes = 32 << 20

// Both first tool feedback and later explicit reads use the same source view
// and line coordinates. Displaying a source never changes its stored bytes.
func agentWorkspaceStructuredView(ctx context.Context, raw []byte, filename string, size int64, input map[string]any) (map[string]any, bool, error) {
	if view, matched, err := agentWorkspaceRecordView(ctx, raw, filename, size, input); matched {
		return view, true, err
	}
	if view, matched, err := agentWorkspaceArticleDocumentView(ctx, raw, filename, size, input); matched {
		return view, true, err
	}
	if view, matched, err := agentWorkspaceHTTPDocumentView(ctx, raw, filename, size, input); matched {
		return view, true, err
	}
	return agentWorkspaceSearchView(ctx, raw, filename, size, input)
}

func agentWorkspaceArticleDocumentView(ctx context.Context, raw []byte, filename string, size int64, input map[string]any) (map[string]any, bool, error) {
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Available, RecordAvailable, SourceUnavailable, Complete bool
			Status, Reason, DOI, PMCID, PMID, Title, AbstractText   string
			AuthorString, PublicationDate, JournalTitle             string
			Source, SourceURL, ContentType, Body, EvidenceState     string
			RecordDepth                                             string
		} `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !envelope.OK {
		return nil, false, nil
	}
	source := envelope.Result
	if strings.TrimSpace(source.SourceURL) == "" || strings.TrimSpace(source.RecordDepth) == "" ||
		(strings.TrimSpace(source.DOI) == "" && strings.TrimSpace(source.PMCID) == "" && strings.TrimSpace(source.PMID) == "") {
		return nil, false, nil
	}
	metadata := map[string]any{
		"source_url": source.SourceURL, "source_size_bytes": size,
		"source_record_depth": source.RecordDepth, "source_evidence_state": source.EvidenceState,
		"source_status": source.Status, "source_unavailable": source.SourceUnavailable,
	}
	media, _, _ := mime.ParseMediaType(source.ContentType)
	citation := sourcecitation.Build(sourcecitation.Input{
		DOI: source.DOI, PMID: source.PMID, PMCID: source.PMCID, Authors: source.AuthorString,
		Title: source.Title, Journal: source.JournalTitle, Published: source.PublicationDate,
	})
	citationHandle, citationText := citation.Handle, citation.Text
	if citationHandle != "" {
		metadata["citation_handle"] = citationHandle
	}
	if citationText != "" {
		metadata["citation_text"] = citationText
	}
	if strings.TrimSpace(source.Body) != "" && (media == "application/xml" || media == "text/xml" || strings.HasSuffix(media, "+xml")) {
		document, err := httptext.JATSDocument(ctx, source.Body)
		if err != nil {
			return nil, true, err
		}
		text := document.ReadingText()
		title := firstNonEmpty(document.Title, source.Title)
		var header strings.Builder
		writeAgentWorkspaceDisplayField(&header, "Title", title)
		writeAgentWorkspaceDisplayField(&header, "Citation handle", citationHandle)
		writeAgentWorkspaceDisplayField(&header, "Citation", citationText)
		if header.Len() > 0 {
			text = strings.TrimSpace(header.String()) + "\n" + text
		}
		metadata["view_format"] = "jats-readable-display-lines"
		metadata["source_complete"] = source.Complete
		metadata["source_content_included"] = strings.TrimSpace(text) != ""
		metadata["raw_read_with"] = agentWorkspaceSourceReadInput(input, "/result/body")
		result, err := readAgentWorkspaceDocumentPage(ctx, text, filename, size, input, metadata)
		return result, true, err
	}
	if !source.RecordAvailable && strings.TrimSpace(source.AbstractText) == "" {
		return nil, false, nil
	}
	var text strings.Builder
	writeAgentWorkspaceDisplayField(&text, "Title", source.Title)
	writeAgentWorkspaceDisplayField(&text, "DOI", source.DOI)
	writeAgentWorkspaceDisplayField(&text, "PMID", source.PMID)
	writeAgentWorkspaceDisplayField(&text, "PMCID", source.PMCID)
	writeAgentWorkspaceDisplayField(&text, "Authors", source.AuthorString)
	writeAgentWorkspaceDisplayField(&text, "Journal", source.JournalTitle)
	writeAgentWorkspaceDisplayField(&text, "Published", source.PublicationDate)
	writeAgentWorkspaceDisplayField(&text, "Source", source.Source)
	writeAgentWorkspaceDisplayField(&text, "Source URL", source.SourceURL)
	writeAgentWorkspaceDisplayField(&text, "Record depth", source.RecordDepth)
	writeAgentWorkspaceDisplayField(&text, "Citation handle", citationHandle)
	writeAgentWorkspaceDisplayField(&text, "Citation", citationText)
	writeAgentWorkspaceDisplayField(&text, "Abstract", source.AbstractText)
	metadata["view_format"] = "article-record-display-lines"
	metadata["source_content_included"] = strings.TrimSpace(source.AbstractText) != ""
	metadata["raw_read_with"] = agentWorkspaceSourceReadInput(input, "/result/abstractText")
	result, err := readAgentWorkspaceDocumentPage(ctx, text.String(), filename, size, input, metadata)
	return result, true, err
}

// The HTTP result contract supplies media type and origin. Do not guess a
// document from field names in arbitrary data or turn failed responses into
// usable source evidence. Explicit JSON selections bypass this reading view.
func agentWorkspaceHTTPDocumentView(ctx context.Context, raw []byte, filename string, size int64, input map[string]any) (map[string]any, bool, error) {
	var envelope struct {
		OK     bool            `json:"ok"`
		Result webfetch.Result `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false, nil
	}
	source := envelope.Result
	media, _, _ := mime.ParseMediaType(source.ContentType)
	if !envelope.OK || source.StatusCode < 200 || source.StatusCode >= 300 || source.SourceUnavailable || source.Binary || source.Error != "" || (media != "text/html" && media != "application/xhtml+xml") {
		return nil, false, nil
	}
	document, err := httptext.HTMLDocument(ctx, source.Body, source.URL)
	if err != nil {
		return nil, true, err
	}
	metadata := map[string]any{
		"source_url": source.URL, "source_content_type": source.ContentType,
		"source_size_bytes": size,
		"view_format":       "html-readable-display-lines",
		"raw_read_with":     agentWorkspaceSourceReadInput(input, "/result/body"),
	}
	text := document.ReadingText()
	if document.Title != "" {
		text = document.Title + "\n" + text
	}
	metadata["source_complete"] = source.Complete || (!source.Partial && !source.Truncated)
	metadata["source_content_included"] = strings.TrimSpace(text) != ""
	// The complete title is already in the paginated text and immutable source.
	// Bound only its duplicate metadata preview so it cannot consume the page.
	metadata["document_title"] = document.Title
	if title := []rune(document.Title); len(title) > 256 {
		metadata["document_title"] = string(title[:256]) + "…"
		metadata["document_title_truncated"] = true
	}
	metadata["source_declared_metadata"] = agentWorkspaceDocumentMetadata(document.Metadata)
	metadata["source_declared_links"] = agentWorkspaceDocumentLinks(document.Links)
	for key, value := range map[string]string{
		"http_requested_url": source.RequestedURL, "http_retrieved_at": source.RetrievedAt,
		"http_response_date": source.ResponseDate, "http_last_modified": source.LastModified,
		"http_etag": source.ETag, "http_content_language": source.ContentLanguage,
		"http_content_location": source.ContentLocation, "source_body_sha256": source.BodySHA256,
		"source_body_hash_scope": source.BodyHashScope,
	} {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if len(source.Redirects) > 0 {
		redirects := make([]any, 0, len(source.Redirects))
		for _, redirect := range source.Redirects {
			redirects = append(redirects, map[string]any{
				"from": redirect.From, "to": redirect.To, "status_code": redirect.StatusCode,
			})
		}
		metadata["http_redirects"] = redirects
	}
	result, err := readAgentWorkspaceDocumentPage(ctx, text, filename, size, input, metadata)
	return result, true, err
}

func agentWorkspaceDocumentMetadata(values []httptext.DocumentMetadata) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{"name": value.Name, "content": value.Content})
	}
	return result
}

func agentWorkspaceDocumentLinks(values []httptext.DocumentLink) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{
			"relation": value.Relation, "url": value.URL, "type": value.Type,
			"language": value.Language, "title": value.Title,
		})
	}
	return result
}

func agentWorkspaceSourceReadInput(input map[string]any, pointer string) map[string]any {
	read := map[string]any{"json_pointer": pointer, "human_description": "Reading original source"}
	if version, ok := input["version_id"]; ok {
		read["version_id"] = version
	} else {
		read["file_path"] = input["file_path"]
	}
	return read
}

func readAgentWorkspaceDocumentPage(ctx context.Context, text, filename string, size int64, input, metadata map[string]any) (map[string]any, error) {
	view := agentWorkspaceSelectedTextView(text)
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	reserved, _ := ctx.Value(agentWorkspaceReadLocationReserveKey{}).(int)
	pageCtx := context.WithValue(ctx, agentWorkspaceReadLocationReserveKey{}, reserved+len(encoded)-1)
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if value, found := input["offset"]; found {
		var ok bool
		offset, ok = strictPositiveAgentWorkspaceInteger(value)
		if !ok {
			return nil, errors.New("read_file.offset is invalid")
		}
	}
	if value, found := input["limit"]; found {
		var ok bool
		limit, ok = strictPositiveAgentWorkspaceInteger(value)
		if !ok {
			return nil, errors.New("read_file.limit is invalid")
		}
	}
	result, err := readAgentWorkspaceTextWindow(pageCtx, bytes.NewReader(view), filename, "text/plain; charset=utf-8", size, offset, limit)
	if err != nil {
		return nil, err
	}
	if result["truncated"] == true && strings.TrimSpace(stringValue(result["content"])) == "" {
		return nil, errors.New("document metadata or display line exceeds the available read response budget")
	}
	for k, v := range metadata {
		result[k] = v
	}
	if !agentWorkspaceReadResultFits(ctx, result) {
		return nil, errors.New("document page exceeds the available read response budget")
	}
	return result, nil
}

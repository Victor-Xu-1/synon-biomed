package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func readAgentWorkspaceOOXML(
	ctx context.Context,
	reader io.ReadSeeker,
	filename string,
	contentType string,
	size int64,
	input map[string]any,
) (any, bool, error) {
	readerAt, cleanup, err := agentWorkspaceReaderAt(ctx, reader, size)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	archive, err := openWebOOXMLArchiveReader(readerAt, size)
	if err != nil {
		return nil, false, nil
	}
	defer archive.Close()
	format, found := agentWorkspaceFormatFromArchive(archive)
	if !found {
		return nil, false, nil
	}
	metadata := agentWorkspaceFormatMetadata(format, "container_signature", "parsed", filename)
	base := map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"reader_contract": metadata,
	}
	switch format.Reader {
	case "builtin_docx":
		content, parseErr := convertWebDOCXArchiveToMarkdown(archive)
		if parseErr != nil {
			return agentWorkspaceStructuredParseFailure(reader, filename, contentType, size, input, format, parseErr), true, nil
		}
		return agentWorkspaceStructuredTextResult(ctx, base, content, input), true, nil
	case "builtin_xlsx":
		workbook, parseErr := convertWebXLSXArchiveToJSON(archive)
		if parseErr != nil {
			return agentWorkspaceStructuredParseFailure(reader, filename, contentType, size, input, format, parseErr), true, nil
		}
		base["workbook"] = workbook
		return base, true, nil
	case "builtin_pptx":
		presentation, parseErr := convertWebPPTXArchiveToJSON(archive)
		if parseErr != nil {
			return agentWorkspaceStructuredParseFailure(reader, filename, contentType, size, input, format, parseErr), true, nil
		}
		base["presentation"] = presentation
		return base, true, nil
	default:
		return nil, false, nil
	}
}

func readAgentWorkspaceHTML(
	ctx context.Context,
	reader io.ReadSeeker,
	filename string,
	contentType string,
	size int64,
	input map[string]any,
	format agentWorkspaceFileFormatContract,
	confidence string,
) (any, error) {
	if _, hasOffset := input["offset"]; hasOffset || size > maxWebFSReadBytes {
		result, err := readAgentWorkspaceRequestedTextWindow(ctx, reader, filename, contentType, size, input)
		if err == nil {
			result["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, "partial_raw_window", filename)
			result["system_hint"] = "This HTML source is shown as a bounded raw window. Use a specialist reader when semantic extraction of the complete large document is required."
		}
		return result, err
	}
	if _, hasLimit := input["limit"]; hasLimit {
		result, err := readAgentWorkspaceRequestedTextWindow(ctx, reader, filename, contentType, size, input)
		if err == nil {
			result["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, "partial_raw_window", filename)
		}
		return result, err
	}
	raw, err := readAgentWorkspaceBounded(ctx, reader, maxWebFSReadBytes+1)
	if err != nil {
		return nil, errors.New("read_file could not read the requested HTML document")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return inspectAgentWorkspaceBinary(reader, filename, contentType, size, input, "The HTML-like source is not valid UTF-8 and requires a verified character-set aware reader."), nil
	}
	readable := webResearchReadableDocument(string(raw), contentType)
	base := map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"title":           webResearchDocumentTitle(string(raw), filepath.Base(filename)),
		"reader_contract": agentWorkspaceFormatMetadata(format, confidence, "parsed", filename),
	}
	return agentWorkspaceStructuredTextResult(ctx, base, readable, input), nil
}

func readAgentWorkspaceDelimited(
	ctx context.Context,
	reader io.ReadSeeker,
	filename string,
	contentType string,
	size int64,
	input map[string]any,
	format agentWorkspaceFileFormatContract,
	confidence string,
) (any, error) {
	if _, hasOffset := input["offset"]; hasOffset || size > maxWebFSReadBytes {
		result, err := readAgentWorkspaceRequestedTextWindow(ctx, reader, filename, contentType, size, input)
		if err == nil {
			result["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, "partial_text_window", filename)
			if size > maxWebFSReadBytes {
				result["system_hint"] = "The delimited source exceeds the full-table parse budget; use line windows or a streaming dataframe reader without copying the original."
			}
		}
		return result, err
	}
	if _, hasLimit := input["limit"]; hasLimit {
		result, err := readAgentWorkspaceRequestedTextWindow(ctx, reader, filename, contentType, size, input)
		if err == nil {
			result["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, "partial_text_window", filename)
		}
		return result, err
	}
	raw, err := readAgentWorkspaceBounded(ctx, reader, maxWebFSReadBytes+1)
	if err != nil {
		return nil, errors.New("read_file could not read the requested delimited table")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return inspectAgentWorkspaceBinary(reader, filename, contentType, size, input, "The table is not valid UTF-8 and requires a verified character-set aware reader."), nil
	}
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	delimiter := ','
	if extension := strings.ToLower(filepath.Ext(filename)); extension == ".tsv" || extension == ".tab" {
		delimiter = '\t'
	} else if mediaType, _, _ := mime.ParseMediaType(contentType); strings.EqualFold(mediaType, "text/tab-separated-values") {
		delimiter = '\t'
	}
	workbook, parseErr := convertWebDelimitedToJSON(bytes.NewReader(raw), strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)), delimiter)
	if parseErr != nil {
		return agentWorkspaceStructuredParseFailure(reader, filename, contentType, size, input, format, parseErr), nil
	}
	return map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"workbook":        workbook,
		"reader_contract": agentWorkspaceFormatMetadata(format, confidence, "parsed", filename),
	}, nil
}

func agentWorkspaceStructuredTextResult(ctx context.Context, base map[string]any, content string, input map[string]any) map[string]any {
	_, hasOffset := input["offset"]
	_, hasLimit := input["limit"]
	if !hasOffset && !hasLimit && len(content) <= agentWorkspaceReadMaxBytes {
		base["content"] = content
		return base
	}
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if raw, found := input["offset"]; found {
		offset, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	if raw, found := input["limit"]; found {
		limit, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	sourceSize, _ := base["size_bytes"].(int64)
	window, err := readAgentWorkspaceTextWindow(ctx, strings.NewReader(content), stringValue(base["filename"]), stringValue(base["content_type"]), sourceSize, offset, limit)
	if err != nil {
		base["content"] = ""
		base["inspection_status"] = "parse_output_unavailable"
		return base
	}
	for key, value := range window {
		base[key] = value
	}
	return base
}

func readAgentWorkspaceRequestedTextWindow(ctx context.Context, reader io.Reader, filename, contentType string, size int64, input map[string]any) (map[string]any, error) {
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if raw, found := input["offset"]; found {
		offset, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	if raw, found := input["limit"]; found {
		limit, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	return readAgentWorkspaceTextWindow(ctx, reader, filename, contentType, size, offset, limit)
}

func agentWorkspaceStructuredParseFailure(
	reader io.ReadSeeker,
	filename string,
	contentType string,
	size int64,
	input map[string]any,
	format agentWorkspaceFileFormatContract,
	parseErr error,
) map[string]any {
	value := inspectAgentWorkspaceBinary(reader, filename, contentType, size, input, "The built-in passive reader rejected this document: "+parseErr.Error())
	value["reader_contract"] = agentWorkspaceFormatMetadata(format, "container_signature", "parse_failed", filename)
	return value
}

func agentWorkspaceReaderAt(ctx context.Context, reader io.ReadSeeker, size int64) (io.ReaderAt, func(), error) {
	if readerAt, ok := reader.(io.ReaderAt); ok {
		return readerAt, func() {}, nil
	}
	if size <= 0 || size == int64(^uint64(0)>>1) {
		return nil, func() {}, errors.New("read_file cannot index a container with an invalid size")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, func() {}, errors.New("read_file could not reset the requested container")
	}
	temporary, err := os.CreateTemp("", "synon-read-container-*")
	if err != nil {
		return nil, func() {}, errors.New("read_file could not stage the requested container")
	}
	cleanup := func() {
		name := temporary.Name()
		_ = temporary.Close()
		_ = os.Remove(name)
	}
	written, copyErr := copyAgentWorkspaceBounded(ctx, temporary, reader, size+1)
	if copyErr != nil || written != size {
		cleanup()
		return nil, func() {}, errors.New("read_file could not stage the complete requested container")
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, func() {}, errors.New("read_file could not index the requested container")
	}
	return temporary, cleanup, nil
}
